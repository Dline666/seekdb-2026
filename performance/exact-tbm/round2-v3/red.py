from common import *
ROOT.mkdir(exist_ok=False)
(ROOT/'bin').mkdir()
for name, original in [('base','base'),('v2','candidate')]:
    shutil.copyfile(PREVIOUS/'bin'/original, ROOT/'bin'/name)
    os.chmod(ROOT/'bin'/name, 0o755)
source=snapshot('red')
command('retention-regression-red', [GO,'test','./internal/sqlparse','./internal/engine',
    '-run','^Test(RowParserReleasesSources|FastInsertReleasesRowReferences)$','-count=1','-v'],
    cwd=source, expected=1)
