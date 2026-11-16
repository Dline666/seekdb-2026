from common import *
import tarfile

label='candidate-fresh-large-retry'
assert json.loads((OUTPUT/(label+'-run.json')).read_text())['validation_passed']
directory=(ROOT/label).resolve()
assert directory.parent==ROOT.resolve() and directory.is_dir()
archive=OUTPUT/(label+'-fixture.tar')
assert not archive.exists()
expected={str(p.relative_to(ROOT)):sha(p) for p in directory.rglob('*') if p.is_file()}
print('large fixture: original hashes recorded',flush=True)
with tarfile.open(archive,'x:') as tar:
    tar.add(directory,arcname=directory.name)
verified={}
with tarfile.open(archive,'r:') as tar:
    for member in tar:
        if member.isfile():
            verified[member.name]=hashlib.file_digest(tar.extractfile(member),'sha256').hexdigest()
assert verified==expected
save(label+'-archive.json',{'archive':str(archive),'sha256':sha(archive),
    'original_directory':str(directory),'files':verified,'reason':'preserve evidence while restoring test-volume space'})
shutil.rmtree(directory)
print('large fixture archived and verified; test volume space restored',flush=True)
