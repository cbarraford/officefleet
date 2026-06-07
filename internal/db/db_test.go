package db

import "testing"

func TestExtractUpBlock(t *testing.T) {
	sql := "-- +migrate Up\nCREATE TABLE foo (id INT);\n-- +migrate Down\nDROP TABLE foo;"
	got := ExtractUpBlock(sql)
	want := "CREATE TABLE foo (id INT);"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtractUpBlock_NoDown(t *testing.T) {
	sql := "-- +migrate Up\nCREATE TABLE bar (id INT);"
	got := ExtractUpBlock(sql)
	want := "CREATE TABLE bar (id INT);"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
