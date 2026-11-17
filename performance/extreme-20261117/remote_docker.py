"""Build the original source Dockerfile and retain image/binary identities."""
import argparse
import fcntl
import json
import os
import subprocess

import lab


def output(argv):
    return subprocess.check_output(argv, text=True).strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--prefix', default='remote-docker')
    args = parser.parse_args()
    assert os.getuid() != 0, 'Build orchestration runs as platemo.'
    assert (lab.ROOT.parent / 'MIGRATION_COMPLETE.json').is_file()
    with (lab.ROOT.parent / 'remote-experiment.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        manifests = []
        for name in ('remote-base', 'remote-final'):
            context = lab.ROOT / 'docker-context' / name
            # Prepared contexts must be identical to the retained snapshots.
            assert lab.sha(context / 'Dockerfile') == lab.sha(lab.REPO / 'Dockerfile')
            source_hashes = json.loads((lab.OUT / (name + '-source.json')).read_text())
            for rel, digest in source_hashes.items():
                assert lab.sha(context / 'minivectordb' / rel) == digest, rel
            image = 'seekdb-remote-20261117:' + name
            lab.run(args.prefix + '-build-' + name,
                    ['docker', 'build', '--progress=plain', '-t', image, str(context)], timeout=3600)
            inspect = json.loads(output(['docker', 'image', 'inspect', image]))[0]
            target = lab.ROOT / 'bin' / (args.prefix + '-' + name)
            assert not target.exists()
            container = output(['docker', 'create', '--name', args.prefix + '-inspect-' + name, image])
            try:
                subprocess.run(['docker', 'cp', container + ':/app/minivectordb', str(target)], check=True)
            finally:
                subprocess.run(['docker', 'rm', '-v', container], check=True)
            manifest = {
                'name': name, 'image': image, 'image_id': inspect['Id'],
                'image_inspect': inspect, 'dockerfile_sha256': lab.sha(context / 'Dockerfile'),
                'source_manifest': name + '-source.json', 'binary_path': str(target),
                'binary_sha256': lab.sha(target), 'native_binary_sha256': lab.sha(lab.ROOT / 'bin' / name),
                'binary_build_info': output([str(lab.GO), 'version', '-m', str(target)]),
                'source_built': True, 'official_submissions': 0,
            }
            manifests.append(manifest)
            lab.save(args.prefix + '-' + name + '-identity.json', manifest)
            print(name, manifest['binary_sha256'], flush=True)
        lab.save(args.prefix + '-identities.json', manifests)


if __name__ == '__main__':
    main()
