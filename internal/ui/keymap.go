package ui

import tea "charm.land/bubbletea/v2"

func isPanelSwitchKey(message tea.KeyPressMsg) bool {
	return message.Key().Code == tea.KeyTab
}

func isCancelKey(message tea.KeyPressMsg) bool {
	key := message.Key()
	return key.Code == 'c' && key.Mod&tea.ModCtrl != 0
}

func isControlKey(message tea.KeyPressMsg, code rune) bool {
	key := message.Key()
	return key.Code == code && key.Mod&tea.ModCtrl != 0
}
