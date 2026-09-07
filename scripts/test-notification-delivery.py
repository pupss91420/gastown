import os, subprocess, tempfile, pathlib, sys
repo=pathlib.Path(__file__).resolve().parents[1]
(repo/".runtime/notification-checks").mkdir(parents=True,exist_ok=True)
with tempfile.TemporaryDirectory(prefix="gt-notification-check-") as sandbox:
 env={k:v for k,v in os.environ.items() if not k.startswith(("GT_","BD_","BEADS_","DOLT_","GASTOWN_","CLAUDE_","CODEX_")) and k not in ("TMUX","TMUX_PANE")}
 env["HOME"]=sandbox
 env["XDG_CONFIG_HOME"]=sandbox+"/config"
 env["GT_DOLT_PORT"]="1"
 env["BEADS_DOLT_PORT"]="1"
 env["DOLT_HOST"]="127.0.0.1"
 env["GT_ROOT"]=sandbox
 env["GT_TOWN_ROOT"]=sandbox
 env["GT_TOWN_SOCKET"]="gt-notification-isolated-"+str(os.getpid())
 env["GOCACHE"]=subprocess.check_output(["go","env","GOCACHE"],text=True).strip()
 env["GOPATH"]=subprocess.check_output(["go","env","GOPATH"],text=True).strip()
 env["GT_TEST_POLLER_BINARY"]=str(repo/".runtime/notification-checks/gt")
 for args in [
  ["go","build","-o",str(repo/".runtime/notification-checks/gt"),"./cmd/gt"],
  ["go","test","./internal/cmd","-run","^(TestNotification|TestShouldSkipDrainUntilIdle|TestEmitEvent|TestWaitForEventFiles|TestReadPendingEvents)","-count=1"],
  ["go","test","./internal/channelevents","./internal/session","./internal/mayor","./internal/nudge","-count=1"],
  ["go","test","./internal/polecat","-run","^TestNotificationPolecat","-count=1"],
  ["go","test","./internal/witness","-run","^TestNotifyRefineryMergeReady_EmitsChannelEvent$","-count=1"],
  ["go","test","./internal/daemon","-run","^TestHasPendingEvents","-count=1"],
  ["go","vet","./internal/channelevents","./internal/session","./internal/mayor","./internal/nudge","./internal/cmd","./internal/witness","./internal/daemon","./internal/polecat"],
 ]:
  print("RUN", " ".join(args),flush=True)
  result=subprocess.run(args,cwd=repo,env=env)
  if result.returncode: sys.exit(result.returncode)
