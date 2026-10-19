package discovery

import (
	"testing"

	"github.com/nulldrive/nulldrive/server/internal/api"
	"github.com/nulldrive/nulldrive/shared/mdns"
)

func TestServiceRecord(t *testing.T) {
	a := &Advertiser{Port: 8080}
	svc := a.Service(api.Advert{Enabled: true, Name: "My NAS", SetupDone: false})
	if svc.Instance != "Null Drive on My NAS" || svc.Host != "nulldrive-my-nas" || svc.Port != 8080 {
		t.Fatalf("%+v", svc)
	}
	txt := mdns.ParseTXT(svc.TXT)
	if svc.TXT[0] != "txtvers=1" || txt["setup"] != "pending" || txt["name"] != "My NAS" || txt["version"] == "" {
		t.Fatalf("txt %v", svc.TXT)
	}
}
