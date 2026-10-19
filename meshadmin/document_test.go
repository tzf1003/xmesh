package meshadmin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const documentPolicy = `{
  // Custom rules, maintained outside xmesh.
  "acls": [{"action":"accept","src":["tag:peer"],"dst":["tag:device:7433"]}],
  "derpMap": {
    "OmitDefaultRegions": true,
    "regions": {
      // Existing cloud relay, not marked as managed.
      "900": {
        "RegionID": 900, "RegionCode": "oracle-jp", "RegionName": "Osaka",
        "Avoid": true,
        "Nodes": [
          {"Name":"900a","RegionID":900,"HostName":"relay.example.com","DERPPort":443,"STUNPort":3478,"Extra":17},
          {"Name":"900b","RegionID":900,"HostName":"backup.example.com","STUNOnly":true},
        ],
      },
    },
  },
  "autoApprovers": {"routes":{"10.0.0.0/8":["tag:device"]}},
}
`

func TestPolicyDocumentCloudRegionsRoundTrip(t *testing.T) {
	doc, err := EditPolicyDocument(DocumentEdit{Text: documentPolicy})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != documentPolicy {
		t.Fatal("loading changed source bytes")
	}
	if !doc.OmitDefault || len(doc.Regions) != 1 || doc.Regions[0].ID != 900 || len(doc.Regions[0].Hosts) != 2 {
		t.Fatalf("wrong region inspection: %+v", doc)
	}
	r := doc.Regions[0]
	r.Text = strings.Replace(r.Text, `"RegionName": "Osaka"`, `"RegionName": "Osaka updated"`, 1)
	updated, err := EditPolicyDocument(DocumentEdit{Text: doc.Text, Region: &DocumentRegionEdit{ID: 900, Text: r.Text}})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(documentPolicy, `"RegionName": "Osaka"`, `"RegionName": "Osaka updated"`, 1)
	if updated.Text != want {
		t.Fatalf("unrelated bytes changed:\n%s", updated.Text)
	}
	if !strings.Contains(updated.Text, `"Extra":17`) || len(updated.Regions[0].Hosts) != 2 {
		t.Fatal("lost node or extension")
	}
}

func TestPolicyDocumentOmitCaseAndAddPreserveCloud(t *testing.T) {
	omit := false
	doc, err := EditPolicyDocument(DocumentEdit{Text: documentPolicy, OmitDefault: &omit, Regions: []DERPRegionSpec{{Code: "lab", Name: "Lab", Hostname: "lab.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.OmitDefault || len(doc.Regions) != 2 || doc.Regions[1].ID != 901 {
		t.Fatalf("wrong merge: %+v", doc)
	}
	if !strings.Contains(doc.Text, `"OmitDefaultRegions": false`) || strings.Contains(doc.Text, `"omitDefaultRegions"`) {
		t.Fatal("case-insensitive field duplicated")
	}
	for _, part := range []string{`"Extra":17`, `"Avoid": true`, `"autoApprovers": {"routes":{"10.0.0.0/8":["tag:device"]}}`, "// Existing cloud relay, not marked as managed."} {
		if !strings.Contains(doc.Text, part) {
			t.Fatalf("lost original content: %s", part)
		}
	}
}

func TestPolicyDocumentRemoveExplicitRegion(t *testing.T) {
	id := 900
	doc, err := EditPolicyDocument(DocumentEdit{Text: documentPolicy, RemoveID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Regions) != 0 || !strings.Contains(doc.Text, `"autoApprovers"`) || !doc.OmitDefault {
		t.Fatal("deletion altered unrelated content")
	}
	if _, err := EditPolicyDocument(DocumentEdit{Text: doc.Text, RemoveID: &id}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing region deletion must fail")
	}
}

func TestPolicyDocumentTemplateComposesWithDERP(t *testing.T) {
	template := TemplatePersonal
	doc, err := EditPolicyDocument(DocumentEdit{Text: documentPolicy, Template: &template, Owner: "autogroup:admin"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := InspectPolicyDocument(documentPolicy)
	if len(doc.Regions) != 1 || doc.Regions[0].Text != before.Regions[0].Text || !strings.Contains(doc.Text, `"autoApprovers"`) {
		t.Fatal("template altered relay or unrelated policy")
	}
	if !strings.Contains(doc.Text, ManagedMarker) {
		t.Fatal("template was not composed")
	}
}

func TestPolicyDocumentRejectsInvalidEdits(t *testing.T) {
	for _, text := range []string{"", " ", "{", `[]`, `{"derpMap":1}`, `{"derpMap":{"regions":[]}}`, `{"derpMap":{"omitDefaultRegions":"true"}}`, `{"derpMap":{"regions":{"900":{"RegionID":901,"RegionCode":"x","RegionName":"X","Nodes":[]}}}}`} {
		if _, err := EditPolicyDocument(DocumentEdit{Text: text}); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted invalid document %q: %v", text, err)
		}
	}
	doc, _ := InspectPolicyDocument(documentPolicy)
	bad := strings.Replace(doc.Regions[0].Text, `"RegionID": 900`, `"RegionID": 901`, 1)
	if _, err := EditPolicyDocument(DocumentEdit{Text: documentPolicy, Region: &DocumentRegionEdit{ID: 900, Text: bad}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted wrong region id")
	}
	if _, err := EditPolicyDocument(DocumentEdit{Text: documentPolicy, Regions: []DERPRegionSpec{{ID: 900, Code: "lab", Name: "Lab", Hostname: "lab.example.com"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("addition overwrote existing relay")
	}
}

func TestPolicyDocumentDeletionWithoutTrailingComma(t *testing.T) {
	std, err := StandardJSON(documentPolicy)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(std, &value); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	id := 900
	if _, err := EditPolicyDocument(DocumentEdit{Text: string(compact), RemoveID: &id}); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyDocumentIPOnlyCloudRelay(t *testing.T) {
	text := strings.Replace(documentPolicy, `"HostName":"relay.example.com"`, `"IPv4":"192.0.2.8"`, 1)
	doc, err := EditPolicyDocument(DocumentEdit{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Text != text || doc.Regions[0].Hosts[0] != "192.0.2.8" {
		t.Fatal("IP-only relay changed or was omitted")
	}
}

func TestPolicyDocumentChangedRelayProbeScope(t *testing.T) {
	rename := strings.Replace(documentPolicy, `"RegionName": "Osaka"`, `"RegionName": "Renamed"`, 1)
	probes, err := ChangedDocumentRelays(documentPolicy, rename)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 0 {
		t.Fatal("display name changes triggered connection probes")
	}
	changed := strings.Replace(documentPolicy, `"DERPPort":443`, `"DERPPort":8443`, 1)
	probes, err = ChangedDocumentRelays(documentPolicy, changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 || probes[0].DERPPort != 8443 || probes[0].Hostname != "relay.example.com" {
		t.Fatalf("wrong changed connection scope: %+v", probes)
	}
}
