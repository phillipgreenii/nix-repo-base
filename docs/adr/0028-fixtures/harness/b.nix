let
  mk =
    name: script:
    derivation {
      inherit name;
      system = builtins.currentSystem;
      builder = "/bin/sh";
      args = [
        "-c"
        script
      ];
    };
in
{
  ok = mk "kq2-ok-${toString builtins.currentTime}" "echo hi; echo > $out";
  slow = mk "kq2-slow-${toString builtins.currentTime}" "echo start; /bin/sleep 1; echo > $out";
  fail1 = mk "kq2-fail1-${toString builtins.currentTime}" "echo boom; exit 3";
  fail2 = mk "kq2-fail2-${toString builtins.currentTime}" "/bin/sleep 1; echo bad >&2; exit 4";
  longfix = mk "kq2-longfix-${builtins.getEnv "KQ2SALT"}" "/bin/sleep 20; echo > $out";
  long = mk "kq2-long-${toString builtins.currentTime}" "/bin/sleep 30; echo > $out";
  chatty = mk "kq2-chatty-${toString builtins.currentTime}" "i=0; while [ $i -lt 20000 ]; do echo line-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; i=$((i+1)); done; echo > $out";
}
