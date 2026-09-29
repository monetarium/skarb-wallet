package dcr

import "testing"

func TestAgendaShownOnInfo(t *testing.T) {
	shown := []AgendaStatusType{AgendaStatusDefined, AgendaStatusStarted}
	hidden := []AgendaStatusType{AgendaStatusLockedIn, AgendaStatusActive, AgendaStatusFailed}
	for _, status := range shown {
		if !AgendaShownOnInfo(status) {
			t.Errorf("%s should show on Info", status)
		}
	}
	for _, status := range hidden {
		if AgendaShownOnInfo(status) {
			t.Errorf("%s should not show on Info", status)
		}
	}
}
