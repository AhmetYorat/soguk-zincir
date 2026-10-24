package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("Demo!2026")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("not argon2id: %s", h)
	}
	ok, err := CheckPassword(h, "Demo!2026")
	if err != nil || !ok {
		t.Fatalf("correct password rejected: %v", err)
	}
	if ok, _ := CheckPassword(h, "demo!2026"); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("Demo!2026")
	if h == h2 {
		t.Fatal("salt not random")
	}
}

func TestCheckPasswordRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "plain", "$2a$10$bcrypt", "$argon2id$v=19$m=x$a$b"} {
		if _, err := CheckPassword(h, "x"); err != ErrInvalidHash {
			t.Fatalf("%q: err = %v", h, err)
		}
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	is := NewIssuer([]byte(strings.Repeat("k", 32)), time.Minute)
	tok, _, err := is.Access(Principal{UserID: 7, Role: RoleCustomer, CustomerID: 3, Email: "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := is.Parse(tok)
	if err != nil || p.UserID != 7 || p.Role != RoleCustomer || p.CustomerID != 3 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestExpiredAndForeignTokensRejected(t *testing.T) {
	is := NewIssuer([]byte(strings.Repeat("k", 32)), time.Minute)
	tok, _, _ := is.Access(Principal{UserID: 1, Role: RoleAuditor})
	is.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := is.Parse(tok); err != ErrInvalidToken {
		t.Fatalf("expired token accepted: %v", err)
	}
	other := NewIssuer([]byte(strings.Repeat("x", 32)), time.Minute)
	tok2, _, _ := other.Access(Principal{UserID: 1, Role: RoleAuditor})
	if _, err := NewIssuer([]byte(strings.Repeat("k", 32)), time.Minute).Parse(tok2); err != ErrInvalidToken {
		t.Fatal("token signed with another secret accepted")
	}
	// alg=none must be refused
	if _, err := is.Parse("eyJhbGciOiJub25lIn0.eyJzdWIiOiIxIn0."); err != ErrInvalidToken {
		t.Fatal("alg none accepted")
	}
}

func TestRBACMatrix(t *testing.T) {
	cases := []struct {
		role Role
		perm Permission
		want bool
	}{
		{RoleQualityManager, PermQualityDecide, true},
		{RoleWarehouseOperator, PermQualityDecide, false},
		{RoleDriver, PermCatalogRead, false},
		{RoleDriver, PermDriverApp, true},
		{RoleCustomer, PermCustomerPortal, true},
		{RoleCustomer, PermTrace, false},
		{RoleAuditor, PermAuditRead, true},
		{RoleAuditor, PermCatalogWrite, false},
		{RoleOperationsManager, PermShipmentsPlan, true},
		{RoleOperationsManager, PermAuditRead, false},
		{Role("root"), PermCatalogRead, false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.perm); got != c.want {
			t.Errorf("Can(%s, %s) = %v", c.role, c.perm, got)
		}
	}
}

func TestAuditorIsReadOnly(t *testing.T) {
	for _, p := range Permissions(RoleAuditor) {
		if !strings.HasSuffix(string(p), ":read") {
			t.Errorf("auditor holds non-read permission %s", p)
		}
	}
}

func TestRefreshTokenHashing(t *testing.T) {
	tok, h, err := NewRefreshToken()
	if err != nil || len(tok) < 40 {
		t.Fatal(err)
	}
	if string(HashRefreshToken(tok)) != string(h) {
		t.Fatal("hash mismatch")
	}
}
