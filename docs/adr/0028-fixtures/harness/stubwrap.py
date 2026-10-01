#!/usr/bin/env python3
# wrapper stub mirroring W-6: child in same pgrp, forward TERM/HUP, never forward INT
import subprocess,signal,sys
log=sys.argv[1]; cmd=sys.argv[2:]
def L(m): open(log,'a').write(m+'\n')
p=subprocess.Popen(cmd)
L(f'wrapper pid={__import__("os").getpid()} child={p.pid}')
def fwd(name,sig):
    def h(s,f): L(f'wrapper got {name}; forwarding'); p.send_signal(sig)
    return h
signal.signal(signal.SIGTERM,fwd('TERM',signal.SIGTERM)); signal.signal(signal.SIGHUP,fwd('HUP',signal.SIGHUP))
signal.signal(signal.SIGINT,lambda s,f: L('wrapper got INT; NOT forwarding'))
while True:
    try: rc=p.wait(); break
    except InterruptedError: pass
L(f'wrapper child exit={rc}'); sys.exit(rc if rc>=0 else 128-rc)
