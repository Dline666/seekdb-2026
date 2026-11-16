from common import *
source=snapshot('red-verified')
command('retention-regression-red-verified', [GO,'test','./internal/sqlparse','./internal/engine',
    '-run','^Test(RowParserReleasesSources|FastInsertReleasesRowReferences)$','-count=1','-v'],
    cwd=source, expected=1)
