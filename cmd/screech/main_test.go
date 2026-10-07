package main

import (
	"strings"
	"testing"
)

func TestParseArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		cmd  command
		rm   bool
		err  string // substring; "" means no error expected
	}{
		{"empty", nil, cmdRun, false, ""},
		{"reduced motion alone", []string{"--reduced-motion"}, cmdRun, true, ""},
		{"doctor", []string{"doctor"}, cmdDoctor, false, ""},
		{"flag before doctor", []string{"--reduced-motion", "doctor"}, cmdDoctor, true, ""},
		{"flag after doctor", []string{"doctor", "--reduced-motion"}, cmdDoctor, true, ""},
		{"help word", []string{"help"}, cmdHelp, false, ""},
		{"help short", []string{"-h"}, cmdHelp, false, ""},
		{"help long", []string{"--help"}, cmdHelp, false, ""},
		{"version word", []string{"version"}, cmdVersion, false, ""},
		{"version short", []string{"-v"}, cmdVersion, false, ""},
		{"version long", []string{"--version"}, cmdVersion, false, ""},
		{"unknown arg", []string{"bogus"}, cmdRun, false, `unknown argument "bogus"`},
		{"unknown flag", []string{"--nope"}, cmdRun, false, `unknown argument "--nope"`},
		{"two commands", []string{"doctor", "version"}, cmdRun, false, "one command"},
		{"two flags fine", []string{"--reduced-motion", "--reduced-motion"}, cmdRun, true, ""},
		{"junk after command", []string{"help", "extra"}, cmdRun, false, `unknown argument "extra"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := parseArgs(tc.args)
			if tc.err == "" {
				if err != nil {
					t.Fatalf("parseArgs(%v) error: %v", tc.args, err)
				}
			} else {
				if err == nil {
					t.Fatalf("parseArgs(%v) should fail", tc.args)
				}
				if !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("parseArgs(%v) error %q, want %q", tc.args, err, tc.err)
				}
				return
			}
			if inv.cmd != tc.cmd {
				t.Errorf("parseArgs(%v).cmd = %v, want %v", tc.args, inv.cmd, tc.cmd)
			}
			if inv.reducedMotion != tc.rm {
				t.Errorf("parseArgs(%v).reducedMotion = %v, want %v", tc.args, inv.reducedMotion, tc.rm)
			}
		})
	}
}
