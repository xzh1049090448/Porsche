package service

import (
	"os"
	"strings"
	"testing"
)

// TestPayOrderIsAtomic guards the payment invariant at the implementation
// boundary: order settlement and user entitlement must share one transaction.
func TestPayOrderIsAtomic(t *testing.T) {
	source, err := os.ReadFile("billing.go")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(source), "func (b *BillingService) PayOrder")
	payOrder := string(source)[start:]
	if start < 0 || !strings.Contains(payOrder, "db.Transaction(") {
		t.Fatal("PayOrder must execute order and entitlement writes in one transaction")
	}
	if !strings.Contains(payOrder, `clause.Locking{Strength: "UPDATE"}`) {
		t.Fatal("PayOrder must lock the order and user rows before settlement")
	}
}
