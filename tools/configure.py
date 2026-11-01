"""Create pinned bridge configuration from a local read-only probe report."""
import argparse
import json
import os
from pathlib import Path
import secrets

p=argparse.ArgumentParser()
p.add_argument('report',type=Path)
p.add_argument('--serial',default='192.168.2.11:4567')
p.add_argument('--bind',default='127.0.0.1')
p.add_argument('--output',type=Path,default=Path('bridge/config.json'))
a=p.parse_args()
info=json.loads(a.report.read_text())['identity']
config={'serial':a.serial,'bind':a.bind,'port':8765,'token':secrets.token_urlsafe(32),
        'allow_control':True,**{'expected_'+k:info[k] for k in ('model','did','mac')}}
# Never overwrite an existing configuration/token accidentally.
fd=os.open(a.output,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,'w') as f:json.dump(config,f,indent=2);f.write('\n')
print('Configuration created:',a.output)
print('API token (enter it in the Home Assistant integration):',config['token'])
