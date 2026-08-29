package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// The polecat-worktree guard must apply to `gt done` and nothing else. Matching
// on the command name alone caught `gt dog done` too, which froze every
// dispatched dog in state=working because no actor could satisfy a polecat-only
// check (gs-4o8).
func TestIsDoneCommandScope(t *testing.T) {
	root := &cobra.Command{Use: "gt"}

	done := &cobra.Command{Use: "done"}
	root.AddCommand(done)

	doneChild := &cobra.Command{Use: "reset"}
	done.AddCommand(doneChild)

	dog := &cobra.Command{Use: "dog"}
	root.AddCommand(dog)
	dogDone := &cobra.Command{Use: "done"}
	dog.AddCommand(dogDone)

	mq := &cobra.Command{Use: "mq"}
	root.AddCommand(mq)
	mqDone := &cobra.Command{Use: "done"}
	mq.AddCommand(mqDone)

	other := &cobra.Command{Use: "status"}
	root.AddCommand(other)

	tests := []struct {
		name string
		cmd  *cobra.Command
		want bool
	}{
		{"gt done is guarded", done, true},
		{"a subcommand of gt done is guarded", doneChild, true},
		{"gt dog done is NOT guarded", dogDone, false},
		{"a done nested under any other parent is NOT guarded", mqDone, false},
		{"an unrelated command is NOT guarded", other, false},
		{"the root command itself is NOT guarded", root, false},
		{"a detached done is guarded (pre-AddCommand / test fixtures)", &cobra.Command{Use: "done"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDoneCommand(tt.cmd); got != tt.want {
				t.Errorf("isDoneCommand(%q under %v) = %v, want %v",
					tt.cmd.Name(), tt.cmd.Parent(), got, tt.want)
			}
		})
	}
}
