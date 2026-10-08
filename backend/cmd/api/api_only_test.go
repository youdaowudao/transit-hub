package main

import (
	"testing"

	"transithub/backend/internal/config"
)

func TestC5APIOnlyListenAddressRemainsLoopbackAndNormalUnchanged(t *testing.T) {
	if got := listenAddress(config.Config{APIOnly: true, Port: "5556"}); got != "127.0.0.1:5556" {
		t.Fatal("API-only backend is not confined to the existing local proxy")
	}
	if got := listenAddress(config.Config{Port: "5555"}); got != ":5555" {
		t.Fatal("normal backend listener changed")
	}
}
