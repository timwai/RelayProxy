package main

import "testing"

func TestSQLiteDSNIsMemory(t *testing.T) {
	cases := []struct {
		dsn  string
		want bool
	}{
		{":memory:", true},
		{"file::memory:?cache=shared", true},
		{"file:test.db?mode=memory&cache=shared", true},
		{"relayproxy.db", false},
		{"file:relayproxy.db", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := sqliteDSNIsMemory(tc.dsn); got != tc.want {
			t.Fatalf("sqliteDSNIsMemory(%q)=%v want %v", tc.dsn, got, tc.want)
		}
	}
}
