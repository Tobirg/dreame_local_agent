"""Exercise the independent interceptor against a local mock firmware library."""
from pathlib import Path
import os
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[1]
SYMBOL='_ZN13avanodemsgcvt9MsgConfig13SetDeviceLockEi'
class RegionGuardTests(unittest.TestCase):
 def test_local_firmware_symbol_and_live_mode_switch(self):
  with tempfile.TemporaryDirectory() as temp:
   p=Path(temp);mock=p/'node_msg_cvt.so';guard=p/'region-guard.so';program=p/'ava'
   (p/'mock.c').write_text(f'void setter(void *p,int code) __asm__("{SYMBOL}");\nvoid setter(void *p,int code){{*(int*)p=code;}}\nvoid call_set(void *p,int code){{setter(p,code);}}\n')
   (p/'test.c').write_text('#include <dlfcn.h>\n#include <stdio.h>\nint main(int argc,char **argv){void*h=dlopen(argv[1],RTLD_NOW|RTLD_LOCAL);if(!h)return 2;void(*call)(void*,int)=dlsym(h,"call_set");if(!call)return 3;int v=17;call(&v,401);printf("%d ",v);call(&v,200);printf("%d\\n",v);return 0;}\n')
   subprocess.run(['gcc','-shared','-fPIC',str(p/'mock.c'),'-o',str(mock)],check=True)
   subprocess.run(['gcc','-shared','-fPIC',str(ROOT/'agent/guard/region_guard.c'),'-ldl',f'-DGUARD_BASE="{p}"',f'-DGUARD_RUNTIME="{p}"',f'-DGUARD_NATIVE_LIBRARY="{mock}"','-o',str(guard)],check=True)
   subprocess.run(['gcc',str(p/'test.c'),'-ldl','-o',str(program)],check=True)
   env={**os.environ,'LD_PRELOAD':str(guard)}
   def result():return subprocess.check_output([str(program),str(mock)],env=env,text=True).strip()
   self.assertEqual(result(),'401 200')
   self.assertTrue((p/'dreame-region-guard.loaded').read_text().strip().isdigit())
   (p/'region-guard.enabled').write_text('1\n')
   self.assertEqual(result(),'17 200')
   self.assertTrue((p/'dreame-region-guard.blocked').is_file())
   (p/'region-guard.enabled').unlink()
   self.assertEqual(result(),'401 200')
if __name__=='__main__':unittest.main()
