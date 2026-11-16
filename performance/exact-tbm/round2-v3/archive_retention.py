from common import *
import tarfile

directory=(ROOT/'retention-v2-memory').resolve()
assert directory.parent==ROOT.resolve() and directory.is_dir()
archive=OUTPUT/'retention-v2-memory-fixture.tar.gz'
assert not archive.exists()
expected={str(p.relative_to(ROOT)):sha(p) for p in directory.rglob('*') if p.is_file()}
with tarfile.open(archive,'x:gz',compresslevel=1) as tar:
    tar.add(directory,arcname=directory.name)
verified={}
with tarfile.open(archive,'r:gz') as tar:
    for member in tar:
        if member.isfile():
            verified[member.name]=hashlib.file_digest(tar.extractfile(member),'sha256').hexdigest()
assert verified==expected
save('retention-archive.json',{'archive':str(archive),'sha256':sha(archive),
    'original_directory':str(directory),'files':verified})
shutil.rmtree(directory)
print('retention fixture archived, verified and removed from test volume')
