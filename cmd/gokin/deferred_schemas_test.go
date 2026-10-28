package main

import "testing"

func TestDeferredSchemaFlagsDefaultOn(t *testing.T) {
	for name, flags := range map[string]*struct{ value, defaultValue string }{
		"headless": {newRootCommand().PersistentFlags().Lookup("deferred-schemas").Value.Type(), newRootCommand().PersistentFlags().Lookup("deferred-schemas").DefValue},
		"eval":     {newEvalRunCmd().Flags().Lookup("deferred-schemas").Value.Type(), newEvalRunCmd().Flags().Lookup("deferred-schemas").DefValue},
	} {
		if flags.value != "bool" || flags.defaultValue != "true" {
			t.Fatalf("%s flag = %+v", name, flags)
		}
	}
}
