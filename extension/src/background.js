// Clicking the toolbar icon opens APIChat in the side panel.
const setup = () => chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true }).catch(() => {});
chrome.runtime.onInstalled.addListener(setup);
chrome.runtime.onStartup.addListener(setup);
