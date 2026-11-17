"""Build the repository Dockerfile from an immutable candidate snapshot."""
import fcntl
import json
import os
import shutil
import subprocess
import sys
import lab

name=sys.argv[1]
assert os.getuid()!=0 and name.replace('-','').isalnum()
image='seekdb-advanced-20261117:'+name
context=lab.RUN/'advanced-docker-context'/name
with (lab.ROOT/'remote-experiment.lock').open('a') as lock:
 fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
 shutil.copytree(lab.RUN/name,context/'minivectordb')
 shutil.copy2(lab.REPO/'Dockerfile',context/'Dockerfile')
 source={str(p.relative_to(context/'minivectordb')):lab.sha(p) for p in sorted((context/'minivectordb').rglob('*')) if p.is_file()}
 lab.save('docker-'+name+'-source.json',source)
 lab.execute('docker-'+name+'-build',['docker','build','--progress=plain','-t',image,context])
 inspect=json.loads(subprocess.check_output(['docker','image','inspect',image],text=True))[0]
 container=subprocess.check_output(['docker','create',image],text=True).strip()
 dest=lab.RUN/'bin'/('docker-'+name)
 assert not dest.exists()
 try:subprocess.run(['docker','cp',container+':/app/minivectordb',str(dest)],check=True)
 finally:subprocess.run(['docker','rm','-v',container],check=True,stdout=subprocess.DEVNULL)
 native=lab.sha(lab.RUN/'bin'/name);actual=lab.sha(dest)
 lab.save('docker-'+name+'-identity.json',{'image':image,'image_id':inspect['Id'],'image_inspect':inspect,'source_manifest':'docker-'+name+'-source.json','dockerfile_sha256':lab.sha(context/'Dockerfile'),'binary_sha256':actual,'native_binary_sha256':native,'identical':actual==native,'official_submissions':0})
 assert actual==native, 'Docker executable differs; do not silently treat it as the native build'
 print(image+': identical '+actual,flush=True)
