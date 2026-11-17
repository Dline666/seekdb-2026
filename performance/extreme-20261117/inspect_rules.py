import json
from pathlib import Path
import re
from urllib.parse import unquote

p = Path(__file__).parent/'results/rules-current.html'
match = re.search(r'decodeURIComponent\("([^\"]+)"\)', p.read_text())
data = json.loads(unquote(match.group(1)))
print(json.dumps({k: data['doc'].get(k) for k in
                  ['id', 'title', 'content_updated_at', 'published_at', 'description']}, ensure_ascii=False))
def walk(value, path=''):
    if isinstance(value, dict):
        for k, v in value.items():
            walk(v, path+'/'+k)
    elif isinstance(value, list):
        for i, v in enumerate(value):
            walk(v, path+'/'+str(i))
    elif isinstance(value, str) and ('3.3.3' in value or '性能排名与得分' in value):
        target = p.parent/'rules-current-body.txt'
        target.write_text(value)
        print(path, len(value), str(target))
walk(data)
