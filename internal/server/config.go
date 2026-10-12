package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	appConfigFileName        = "config.json"
	defaultManagerPort       = "3000"
	envEchoKitDir            = "ECHOKIT_DIR"
	envManagerPort           = "PORT"
	envManagerDataDir        = "ECHOMANAGER_DATA_DIR"
	managerDataDirectoryName = ".echomanager-data"
)

type appConfig struct {
	Port               string
	EchoKitDir         string
	EchoKitDirExplicit bool
	EchoKitDirFromEnv  bool
	DataDir            string
	ConfigPath         string
	ConfigFileExists   bool
	File               appConfigFile
}

type appConfigFile struct {
	Port       string `json:"port"`
	EchoKitDir string `json:"echokit_dir"`
	DataDir    string `json:"data_dir"`
}

func loadAppConfig() (appConfig, error) {
	executable, _ := os.Executable()
	workingDir, _ := os.Getwd()
	configPath := findAppConfigPath(executable, workingDir)
	return loadAppConfigAt(configPath, executable, workingDir, os.LookupEnv)
}

func findAppConfigPath(executablePath, workingDir string) string {
	candidates := make([]string, 0, 2)
	if executablePath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(executablePath), appConfigFileName))
	}
	if workingDir != "" {
		workingConfig := filepath.Join(workingDir, appConfigFileName)
		if len(candidates) == 0 || workingConfig != candidates[0] {
			candidates = append(candidates, workingConfig)
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	if workingDir != "" {
		if _, err := os.Stat(filepath.Join(workingDir, "go.mod")); err == nil {
			return filepath.Join(workingDir, appConfigFileName)
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return appConfigFileName
}

func loadAppConfigAt(configPath, executablePath, workingDir string, lookupEnv func(string) (string, bool)) (appConfig, error) {
	fileConfig := appConfigFile{}
	configFileExists := false
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err == nil {
			configFileExists = true
			if err := json.Unmarshal(data, &fileConfig); err != nil {
				return appConfig{}, fmt.Errorf("parse %s: %w", configPath, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return appConfig{}, fmt.Errorf("read %s: %w", configPath, err)
		}
	}

	configDir := workingDir
	if configPath != "" {
		configDir = filepath.Dir(configPath)
	}
	port := configuredValue(lookupEnv, envManagerPort, fileConfig.Port)
	if port == "" {
		port = defaultManagerPort
	}

	envKitDir, kitDirFromEnv := lookupEnv(envEchoKitDir)
	kitDirFromEnv = kitDirFromEnv && strings.TrimSpace(envKitDir) != ""
	kitDirExplicit := kitDirFromEnv || strings.TrimSpace(fileConfig.EchoKitDir) != ""
	kitDir := configuredValue(lookupEnv, envEchoKitDir, fileConfig.EchoKitDir)
	if kitDir == "" {
		kitDir = defaultKitDir(executablePath, workingDir)
	} else if !filepath.IsAbs(kitDir) {
		base := configDir
		if envValue, isSet := lookupEnv(envEchoKitDir); isSet && strings.TrimSpace(envValue) != "" {
			base = workingDir
		}
		kitDir = filepath.Join(base, kitDir)
	}

	configuredDataDir := configuredValue(lookupEnv, envManagerDataDir, fileConfig.DataDir)
	dataDir := configuredDataDir
	if dataDir == "" {
		var err error
		dataDir, err = defaultManagerDataDir(executablePath, configPath, workingDir)
		if err != nil {
			return appConfig{}, err
		}
	} else {
		base := configDir
		if envValue, isSet := lookupEnv(envManagerDataDir); isSet && strings.TrimSpace(envValue) != "" {
			base = workingDir
		}
		if !filepath.IsAbs(dataDir) {
			dataDir = filepath.Join(base, dataDir)
		}
		var err error
		dataDir, err = ensureDataDirectory(dataDir)
		if err != nil {
			return appConfig{}, err
		}
	}

	return appConfig{
		Port:               port,
		EchoKitDir:         filepath.Clean(kitDir),
		EchoKitDirExplicit: kitDirExplicit,
		EchoKitDirFromEnv:  kitDirFromEnv,
		DataDir:            dataDir,
		ConfigPath:         configPath,
		ConfigFileExists:   configFileExists,
		File:               fileConfig,
	}, nil
}

func writeAppConfig(path string, config appConfigFile) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func configuredValue(lookupEnv func(string) (string, bool), key, fallback string) string {
	if value, isSet := lookupEnv(key); isSet && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func defaultManagerDataDir(executablePath, configPath, workingDir string) (string, error) {
	programDir := filepath.Dir(executablePath)
	if workingDir != "" {
		if _, err := os.Stat(filepath.Join(workingDir, "go.mod")); err == nil {
			programDir = workingDir
		}
	}
	if programDir == "." || programDir == "" {
		programDir = filepath.Dir(configPath)
	}
	dataDir := filepath.Join(programDir, managerDataDirectoryName)
	legacyDir, legacyErr := legacyManagerDataDir()
	if legacyErr != nil {
		return "", legacyErr
	}
	if err := migrateLegacyManagerDataDir(legacyDir, dataDir); err != nil {
		return "", err
	}
	return ensureDataDirectory(dataDir)
}

func migrateLegacyManagerDataDir(legacyDir, destination string) error {
	if filepath.Clean(legacyDir) == filepath.Clean(destination) {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(legacyDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := copyDataDirectory(legacyDir, destination); err != nil {
		return fmt.Errorf("migrate existing EchoManager data to %s: %w", destination, err)
	}
	return nil
}

func legacyManagerDataDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "EchoManager"), nil
}

func copyDataDirectory(source, destination string) error {
	temporary, err := os.MkdirTemp(filepath.Dir(destination), ".echomanager-data-migrate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	err = filepath.WalkDir(source, func(sourcePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, sourcePath)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(temporary, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(sourcePath)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	return nil
}

func ensureDataDirectory(path string) (string, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return "", err
	}
	return filepath.Abs(path)
}

func defaultKitDir(executablePath, workingDir string) string {
	candidates := make([]string, 0, 3)
	if executablePath != "" {
		executableDir := filepath.Dir(executablePath)
		candidates = append(candidates,
			filepath.Join(executableDir, "EchoKit"),
			filepath.Join(filepath.Dir(executableDir), "EchoKit"),
		)
	}
	if workingDir != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(workingDir), "EchoKit"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return "EchoKit"
}
