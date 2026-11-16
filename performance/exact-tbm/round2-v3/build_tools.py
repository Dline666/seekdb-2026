from common import *
command('format-tbm-driver', ['/tmp/seekdb-round3-go/bin/gofmt','-w',OUTPUT/'tbm_eval.go'])
command('build-tbm-driver', [GO,'build','-trimpath','-o',ROOT/'bin/tbm-eval',OUTPUT/'tbm_eval.go'])
shutil.copyfile(ROOT/'bin/base', ROOT/'bin/base-copy')
os.chmod(ROOT/'bin/base-copy', 0o755)
images={}
for name in ['base','v2','memory','candidate']:
    context=ROOT/'docker'/name
    context.mkdir(parents=True)
    shutil.copyfile(ROOT/'bin'/name, context/'minivectordb')
    os.chmod(context/'minivectordb', 0o755)
    (context/'Dockerfile').write_text('FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852\n'
        'WORKDIR /app\nCOPY minivectordb /app/minivectordb\nENTRYPOINT ["/app/minivectordb"]\nCMD ["--data-dir","/data"]\n')
    image='seekdb-local-v3:'+name
    command('image-'+name, ['docker','build','--network=none','-t',image,context],extra={'DOCKER_BUILDKIT':'0'})
    image_id=subprocess.check_output(['docker','image','inspect','--format','{{.Id}}',image],text=True).strip()
    images[name]={'image':image,'image_id':image_id,'binary_sha256':sha(context/'minivectordb')}
command('image-smoke-copy',['docker','tag','seekdb-local-v3:base','seekdb-local-v3:base-copy'])
save('images.json', images)
save('docker-environment.json', json.loads(subprocess.check_output(['docker','info','--format','{{json .}}'])))
