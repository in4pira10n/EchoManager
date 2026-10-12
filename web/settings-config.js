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
    managerPreferences: { confirmStop: false },
    settingsPanels: { appearance: true, network: true, about: true },
  },
  appearance: {
    themeOptions: [
      { id: "system", label: "系统" },
      { id: "light", label: "浅色" },
      { id: "dark", label: "深色" },
    ],
    themeColors: { dark: "#1a1a1a", light: "#f2f2f7" },
    systemAccentDescription: "系统配色方案",
    customAccentDescription: "自定义配色方案",
  },
  panels: {
    appearance: { icon: "sun.min" },
    network: { icon: "network" },
    about: { icon: "gear" },
  },
  copy: {
    pageLabel: "管理器设置",
    manager: {
      originLabel: "访问地址",
      instanceCountLabel: "已登记服务",
      instanceCountSuffix: " 个",
    },
    appearance: {
      title: "颜色与外观",
      description: "调整页面的基本显示设置",
      themeLabel: "颜色主题",
      themeDescription: "选择是否随系统切换深浅色",
      accentLabel: "主色",
      accentPickerTitle: "选择主色",
      accentPickerLabel: "选择主色",
    },
    taskStatus: {
      confirmStopLabel: "停止前确认",
      confirmStopDescription: "减少误操作导致的服务中断",
    },
    network: {
      title: "局域网",
      description: "配置通用网络控制",
      devicesLabel: "设备列表",
      devicesDescription: "局域网设备展示",
      deviceCount: "当前共 10 台设备",
    },
    about: {
      title: "一般",
      description: "通用设置",
      versionLabel: "EchoManager 版本",
      version: "v0.0.2",
      checkUpdates: "检查更新",
      adminLabel: "当前管理员",
    },
  },
};
