package affect

import (
	"reflect"
	"testing"

	"github.com/yug/unbroke/internal/change"
	"github.com/yug/unbroke/internal/scan"
)

func chg(rule string, sev change.Severity, summary string, ops ...string) change.Change {
	c := change.Change{RuleID: rule, Severity: sev, Summary: summary, Operations: ops}
	if len(ops) > 0 {
		c.Method, c.Path, _ = splitOp(ops[0])
	}
	return c
}

func splitOp(op string) (string, string, bool) {
	for i := 0; i < len(op); i++ {
		if op[i] == ' ' {
			return op[:i], op[i+1:], true
		}
	}
	return op, "", false
}

func call(op, file string, line int, window string) scan.Match {
	return scan.Match{Operation: op, File: file, Line: line, Kind: scan.KindSDK, Confidence: scan.Medium, Window: window}
}

func TestFieldNames_FromRealStripeSentences(t *testing.T) {
	cases := []struct {
		summary string
		want    []string
	}{
		{"removed the request property `payment_method_types`", []string{"payment_method_types"}},
		{"added the new required request property `subscription_details/billing_cycle_anchor/type`", []string{"billing_cycle_anchor"}},
		{"removed the enum value `custom` of the request property `ui_mode`", []string{"custom", "ui_mode"}},
		{"the response property `signals/fraudulent_payment/score` became nullable for the status `200`", []string{"score", "fraudulent_payment"}},
		{"the response property `a` became optional for the status `200` (media type: application/json)", nil},
	}
	for _, c := range cases {
		got := fieldNames(change.Change{Summary: c.summary})
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("fieldNames(%q) = %v, want %v", c.summary, got, c.want)
		}
	}
}

func TestHasWord(t *testing.T) {
	cases := []struct {
		s, w string
		want bool
	}{
		{"payment_method_types: ['card']", "payment_method_types", true},
		{"my_payment_method_types", "payment_method_types", false},
		{"payment_method_types_extra", "payment_method_types", false},
		{"x.ui_mode = 1", "ui_mode", true},
		{"build_ui_mode", "ui_mode", false},
		{"ui_mode", "ui_mode", true},
		{"", "ui_mode", false},
	}
	for _, c := range cases {
		if got := hasWord(c.s, c.w); got != c.want {
			t.Errorf("hasWord(%q, %q) = %v, want %v", c.s, c.w, got, c.want)
		}
	}
}

func TestCorrelate_DropsChangesTheCodeDoesNotCall(t *testing.T) {
	changes := []change.Change{
		chg("request-property-removed", change.Warning, "removed the request property `charge`", "POST /v1/refunds"),
		chg("request-property-removed", change.Warning, "removed the request property `payto`", "POST /v1/payment_methods/{id}"),
	}
	ms := []scan.Match{call("POST /v1/refunds", "a.js", 3, "stripe.refunds.create({ amount: 1 })")}
	got := Correlate("stripe", changes, ms)
	if len(got) != 1 || got[0].Change.Summary != changes[0].Summary {
		t.Fatalf("got %+v, want only the refunds change", got)
	}
}

func TestCorrelate_FieldNearTheCallIsRankedAbove(t *testing.T) {
	changes := []change.Change{
		chg("request-property-removed", change.Warning, "removed the request property `charge`", "POST /v1/refunds"),
		chg("request-property-removed", change.Warning, "removed the request property `metadata`", "POST /v1/refunds"),
	}
	ms := []scan.Match{call("POST /v1/refunds", "a.js", 3, "stripe.refunds.create({ charge: ch, amount: 1 })")}
	got := Correlate("stripe", changes, ms)
	if len(got) != 2 {
		t.Fatalf("got %d findings", len(got))
	}
	if got[0].Relevance != ChangedField || got[0].Hits[0].Field != "charge" {
		t.Errorf("first = %+v, want the finding whose field appears in the code", got[0])
	}
	if got[1].Relevance != CallsEndpoint {
		t.Errorf("second = %+v, want calls-endpoint: the code never mentions metadata", got[1])
	}
}

func TestCorrelate_RemovedEndpointOutranksEverything(t *testing.T) {
	changes := []change.Change{
		chg("request-property-removed", change.Breaking, "removed the request property `charge`", "POST /v1/refunds"),
		chg("api-path-removed-without-deprecation", change.Breaking, "api path removed without deprecation", "POST /v1/charges"),
	}
	ms := []scan.Match{
		call("POST /v1/refunds", "a.js", 3, "refunds.create({charge: 1})"),
		call("POST /v1/charges", "b.js", 9, "charges.create({})"),
	}
	got := Correlate("stripe", changes, ms)
	if got[0].Relevance != RemovedEndpoint || got[0].Change.RuleID != "api-path-removed-without-deprecation" {
		t.Errorf("first = %+v, want the removed endpoint", got[0])
	}
}

func TestCorrelate_ChangeSharedByManyOperationsMatchesAnyOfThem(t *testing.T) {
	c := chg("request-property-removed", change.Warning, "removed the request property `payment_method_types`", "POST /v1/payment_intents", "POST /v1/setup_intents")
	ms := []scan.Match{
		call("POST /v1/setup_intents", "a.py", 4, "stripe.SetupIntent.create(payment_method_types=['card'])"),
	}
	got := Correlate("stripe", []change.Change{c}, ms)
	if len(got) != 1 || got[0].Relevance != ChangedField {
		t.Fatalf("got %+v", got)
	}
}

func TestCorrelate_NothingFoundMeansNothingReported(t *testing.T) {
	if got := Correlate("stripe", []change.Change{chg("r", change.Breaking, "x `y`", "GET /a")}, nil); len(got) != 0 {
		t.Errorf("got %+v, want none", got)
	}
}
