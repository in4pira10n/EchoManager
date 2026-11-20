window.ECHO_MANAGER_SETTINGS = {
  storageKeys: {
    theme: "em-theme",
    accentColor: "em-accent-color",
    managerPreferences: "em-manager-preferences",
    settingsPanels: "em-settings-panels",
  },
  defaults: {
    theme: "system",
    accentColor: "#34c759",
    startTimeoutSeconds: 25,
    managerPreferences: { confirmStop: false, confirmShortcut: true },
    settingsPanels: { appearance: true, network: true, about: true },
  },
  appearance: {
    startTimeoutRange: { minSeconds: 5, maxSeconds: 120 },
    themeOptions: [
      { id: "system", key: "settings.themeSystem" },
      { id: "light", key: "settings.themeLight" },
      { id: "dark", key: "settings.themeDark" },
    ],
    themeColors: { dark: "#1a1a1a", light: "#f2f2f7" },
  },
  panels: {
    appearance: { icon: "sun.min" },
    network: { icon: "network" },
    about: { icon: "gear" },
  },
};
