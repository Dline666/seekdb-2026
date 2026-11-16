from common import *
files=['internal/sqlparse/insert_fast.go','internal/sqlparse/insert_retention_test.go',
       'internal/engine/insert_fast.go','internal/engine/insert_retention_test.go']
command('format-memory-fix', ['/tmp/seekdb-round3-go/bin/gofmt','-w',
    *[str(REPO/'minivectordb'/p) for p in files]])
source=snapshot('memory')
command('retention-regression-green', [GO,'test','./internal/sqlparse','./internal/engine',
    '-run','^Test(RowParserReleasesSources|FastInsertReleasesRowReferences)$','-count=1','-v'],cwd=source)
command('build-memory', [GO,'build','-trimpath','-o',ROOT/'bin/memory','./cmd/minivectordb'],cwd=source)
save('memory-binary.json', {'sha256':sha(ROOT/'bin/memory'), 'default_spin_us':1000,
     'change':'Only completed INSERT source reference release compared with f723ad1'})
