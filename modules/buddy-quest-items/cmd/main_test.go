package main

import (
	"context"
	"strconv"
	"strings"
	"testing"

	buddyquestitems "github.com/agent-wow/agent-wow/modules/buddy-quest-items"
	"google.golang.org/protobuf/types/known/emptypb"
)

func runtimeEnvironment() map[string]string {
	return map[string]string{
		"AGENT_WOW_MODULE_SOCKET":  "/run/agent-wow/module.sock",
		"AGENT_WOW_SESSION_SOCKET": "/run/agent-wow/session.sock",
		"AGENT_WOW_SESSION_ID":     "aw-1431887544",
		"AGENT_WOW_CHARACTER_GUID": "4",
	}
}

func TestQuestItemsRuntimeIdentityUsesCanonicalBodySession(t *testing.T) {
	env := runtimeEnvironment()
	for _, guid := range []uint64{1, 4, 5, 6, 8} {
		env["AGENT_WOW_CHARACTER_GUID"] = strconv.FormatUint(guid, 10)
		for _, session := range []string{"aw-1431887544", "aw-1431887545", "aw-example_01.session"} {
			env["AGENT_WOW_SESSION_ID"] = session
			config, err := readRuntimeConfig(func(key string) string { return env[key] })
			if err != nil || config.self != guid || config.sessionID != session {
				t.Fatal(config, err)
			}
			service := buddyquestitems.NewService(nil, config.sessionID, config.self)
			state, err := service.GetState(context.Background(), &emptypb.Empty{})
			if err != nil || state.SessionId != session || state.SelfGuid != guid {
				t.Fatal(state, err)
			}
		}
	}
}

func TestQuestItemsRuntimeRejectsMissingOrInvalidIdentityBeforeSockets(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"AGENT_WOW_MODULE_SOCKET", ""}, {"AGENT_WOW_SESSION_SOCKET", ""},
		{"AGENT_WOW_SESSION_ID", ""}, {"AGENT_WOW_SESSION_ID", strings.Repeat("x", 129)},
		{"AGENT_WOW_SESSION_ID", "aw-1\n"}, {"AGENT_WOW_SESSION_ID", "../aw-1"},
		{"AGENT_WOW_SESSION_ID", "aw-1/foreign"}, {"AGENT_WOW_SESSION_ID", " aw-1"},
		{"AGENT_WOW_CHARACTER_GUID", ""}, {"AGENT_WOW_CHARACTER_GUID", "0"},
		{"AGENT_WOW_CHARACTER_GUID", "-1"}, {"AGENT_WOW_CHARACTER_GUID", "not-a-guid"},
		{"AGENT_WOW_CHARACTER_GUID", "281474976710656"},
	} {
		t.Run(tc.key+"/"+strconv.Quote(tc.value), func(t *testing.T) {
			env := runtimeEnvironment()
			env[tc.key] = tc.value
			config, err := readRuntimeConfig(func(key string) string { return env[key] })
			if err == nil || config != (runtimeConfig{}) {
				t.Fatal("invalid runtime identity accepted", config, err)
			}
		})
	}
}
