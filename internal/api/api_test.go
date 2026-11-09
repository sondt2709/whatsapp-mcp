package api

import (
	"fmt"
	"testing"
)

func TestLimit(t *testing.T) {
	for in, want := range map[int]int{-1: DefaultLimit, 0: DefaultLimit, 1: 1, 500: 500, 501: MaxLimit} {
		if got := Limit(in); got != want {
			t.Errorf("Limit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestWait(t *testing.T) {
	for in, want := range map[int]int{0: DefaultWait, 5: 5, 600: 600, 601: MaxWait} {
		if got := Wait(in); got != want {
			t.Errorf("Wait(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCodeOf(t *testing.T) {
	err := fmt.Errorf("wrap: %w", Errorf(FileExpired, "the file is only on the phone now"))
	if got := CodeOf(err); got != FileExpired {
		t.Fatalf("CodeOf = %q, want %q", got, FileExpired)
	}
	if got := CodeOf(fmt.Errorf("plain")); got != Internal {
		t.Fatalf("CodeOf(plain) = %q, want %q", got, Internal)
	}
}
