package server

import "embed"

// Remote scripts remain executable resources, separate from translatable UI copy.
//
//go:embed resources/*.sh resources/*.ps1
var remoteScripts embed.FS

func remoteScript(name string) string {
	data, err := remoteScripts.ReadFile("resources/" + name)
	if err != nil {
		panic(err)
	}
	return string(data)
}
