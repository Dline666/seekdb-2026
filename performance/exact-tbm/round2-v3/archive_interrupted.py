from common import *
import tarfile

label='candidate-fresh-large'
directory=(ROOT/label).resolve()
assert directory.parent==ROOT.resolve() and directory.is_dir()
archive=OUTPUT/(label+'-interrupted-fixture.tar')
assert not archive.exists()
partial=[json.loads(line) for line in (OUTPUT/(label+'.jsonl')).read_text().splitlines()]
assert partial[-1]['phase']=='recovery_after_bulk_commit'
save(label+'-interruption.json',{'status':'interrupted, not a full-workflow pass',
    'last_recorded_phase':partial[-1]['phase'],'reason':'WSL restarted; both long-lived WSL execution sessions exited 1; trigger unconfirmed',
    'binary_sha256':sha(ROOT/'bin/candidate'), 'partial_record_count':len(partial),
    'last_recorded_memory_events':partial[-1]['memory_events'],
    'original_run_and_summary_files_empty':True})
expected={str(p.relative_to(ROOT)):sha(p) for p in directory.rglob('*') if p.is_file()}
print('interrupted fixture: hashes recorded',flush=True)
with tarfile.open(archive,'x:') as tar:
    tar.add(directory,arcname=directory.name)
verified={}
with tarfile.open(archive,'r:') as tar:
    for member in tar:
        if member.isfile():
            verified[member.name]=hashlib.file_digest(tar.extractfile(member),'sha256').hexdigest()
assert expected==verified
save(label+'-interrupted-archive.json',{'archive':str(archive),'sha256':sha(archive),'files':verified,
    'original_directory':str(directory),'status':'incomplete workload, preserved for diagnosis'})
shutil.rmtree(directory)
print('interrupted data archived and verified',flush=True)
