# 选择性暂存(方案D,逐文件精确构造):HEAD 内容 + 仅本次 storage 新增 → 写 index。
# 背景:四个共享文件混有其他会话在途 WIP,绝不能整文件提交。
import subprocess

def head(path):
    return subprocess.run(['git', 'show', 'HEAD:' + path], capture_output=True, text=True,
                          encoding='utf-8', errors='replace').stdout

def work(path):
    with open(path, encoding='utf-8', errors='replace') as f:
        return f.read()

def stage(path, content):
    p = subprocess.run(['git', 'hash-object', '-w', '--stdin'], input=content.encode('utf-8'),
                       capture_output=True)
    sha = p.stdout.decode().strip()
    subprocess.run(['git', 'update-index', '--add', '--cacheinfo', '100644,' + sha + ',' + path],
                   check=True)
    print('staged', path, sha[:12], len(content), 'bytes')

def suffix_from(text, marker):
    i = text.index(marker)
    return text[i:]

# 1) schema.sql = HEAD + 追加块(块文件由实现时落盘)
blk = open('tools/_storage_schema_block.sql', encoding='utf-8').read()
s = head('db/schema.sql')
if not s.endswith('\n'):
    s += '\n'
stage('db/schema.sql', s + blk)

# 2) types.ts = HEAD + 工作区中「存储中心」标记起的纯 EOF 追加段
s = head('web/src/lib/types.ts')
if not s.endswith('\n'):
    s += '\n'
stage('web/src/lib/types.ts', s + '\n' + suffix_from(work('web/src/lib/types.ts'),
      '// ============ 存储中心(契约 STORAGE_CONTRACTS.md) ============\n'))

# 3) api.ts = HEAD + import 类型名 + api 对象尾部追加 storage 段
w = work('web/src/lib/api.ts')
sec = suffix_from(w, '  // ---- 存储中心(契约 STORAGE_CONTRACTS.md) ----\n')
# 工作区里 storage 段之后只剩 api 对象收口的 "};",剥掉后即纯段体
assert sec.rstrip().endswith('};')
sec = sec.rstrip()[:-2].rstrip() + '\n'
s = head('web/src/lib/api.ts')
old = '  TaskConstraintInput,\n  TaskInputParse,\n} from "@/lib/types";'
assert s.count(old) == 1
s = s.replace(old, '  TaskConstraintInput,\n  TaskInputParse,\n'
                   '  StorageBucket,\n  StorageBucketInput,\n  StorageCategory,\n'
                   '  StorageCategoryOverview,\n  StorageMigration,\n} from "@/lib/types";', 1)
assert s.endswith('};\n')
s = s[:-3] + sec + '};\n'
stage('web/src/lib/api.ts', s)

# 4) sidebar = HEAD + 图标 import + 系统设置组子项(两处精确替换)
s = head('web/src/navigation/sidebar/sidebar-items.ts')
old = '  Gauge,\n  Gem,'
assert s.count(old) == 1
s = s.replace(old, '  Gauge,\n  Gem,\n  HardDrive,', 1)
old = '          { id: "functions", title: "系统功能", url: "/system/functions", icon: SlidersHorizontal },\n'
assert s.count(old) == 1
s = s.replace(old, old +
              '          // 存储设置:对象存储桶接入(S3/WebDAV)+历史数据一键迁移。\n'
              '          { id: "storage", title: "存储设置", url: "/system/storage", icon: HardDrive },\n', 1)
stage('web/src/navigation/sidebar/sidebar-items.ts', s)
