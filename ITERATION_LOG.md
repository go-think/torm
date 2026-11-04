# torm 对标 Laravel 13 迭代日志

> 目标：在不提交的前提下，通过「实现 → 检查（build/vet/fmt/test/golden）→ 修正」的反复迭代，把 torm 的行为对齐 Laravel 13 `Illuminate/Database`。
> 行为基准：/tmp/laravel（laravel/framework 浅克隆），golden 断言逐条移植自 `tests/Database/DatabaseQueryBuilderTest.php` 与 `src/Illuminate/Database` 源码。
> 最终状态：**12 轮迭代，286 条测试断言全绿**（go build / go vet / gofmt / go test 全部干净）。

## R1 测试基建

- `connection_test.go`：init() 无 MySQL 时不再 panic（Ping 探测）；新增 `skipNoDB(t)`，依赖真实 MySQL 的用例自动 Skip。
- 新增 `fake_test.go`：内存假 driver（driver.Conn/Stmt/Rows/Tx，调用记录、错误注入），Connection 层完全脱离 MySQL 可测。
- **实测修复 `[]*T` 扫描 bug**：多行时 panic（`Addr()` 不可寻址）——比审读怀疑的"全部指向同一元素"更严重；改为每行分配全新元素。
- struct 目标扫描修正（行数据正确回写 dest）；非 struct 元素类型 panic 改为报错，并新增标量扫描路径（pluck 场景）。

## R2 SQL 编译层重构（本轮最大改动）

- `query` 包重写：Query/Where/Order/Join/Union/With/IndexHint/Aggregate 与 Laravel `Query\Builder` 属性一一对应（nil=未设置、Lock=bool|string、Distinct=bool|列数组）。
- `grammar` 三层重构：`BaseGrammar`（wrap/参数/表达式，默认双引号=SQL 标准方言）→ `QueryGrammar`（共享编译，selectComponents 顺序一致）→ `MySqlGrammar`（反引号/JSON/fulltext/index hint/insert ignore/upsert/update-delete 附加 order limit）。方言差异用「构造器注入钩子」实现（Go 无虚方法分派）。
- SQL 输出风格对齐：小写关键字、标识符全 wrap（含 `as` 别名、schema、引号转义）、`removeLeadingBoolean` 只剥首个 and/or。
- builder 重写：Where 管线完整移植（操作符回退交换、null→whereNull、闭包列→嵌套分组、子查询列/值→whereSub、数组值 flattenValue、JSON 列布尔值→raw、`<=>`→NullSafeEquals）；`whereNot` 用 boolean 后缀 `and not`；whereIn 子查询值+嵌套数组校验；whereBetween 绑定 flatten+slice(0,2)。
- 绑定体系：9 segment（select/from/join/where/groupBy/having/order/union/unionOrder）固定顺序合并（**修复 P0-1 随机序**）；未知段 panic。
- **修复 P0-4**：Clone/CloneWithout/CloneWithoutBindings 真克隆，Aggregate 不再污染原查询。
- **修复 P0-2/P0-3**：WhereRaw 正常编译；Join 变参错位修复 + Left/Right/CrossJoin/JoinWhere/JoinSub 家族。
- 新增：SelectSub/FromSub/FromRaw/Distinct/When/Unless/SetAggregate/Exists/DoesntExist/InsertGetId/InsertOrIgnore/InsertUsing/Upsert/UpdateOrInsert/Increment/Decrement/Truncate/GetRawBindings/SetBindings/MergeBindings。
- 新增 `query.Row`（KV 有序行）对应 PHP 有序数组；map 输入确定性排序。
- prefix 接线（修复 P0-7）；`prepareBindings` 过滤 Expression；`InsertGetId`。
- 检查：154 条 golden（双方言）全绿。

## R3 insert/update/delete 执行级 golden

- 假 driver 增加调用记录（SQL+参数）；`statements_test.go` 断言 Connection 实收内容，全部来自 Laravel 测试断言。
- 覆盖：insert 单行/多行/表达式值不进绑定/空值 `() values ()`/insertGetId/insertOrIgnore/insertUsing（空列+子查询绑定）/upsert（on duplicate key update）/update（绑定顺序、order+limit、Raw 内联）/increment/decrement（`"balance" + 1` 内联、附加列）/delete（delete(id) 主键限定、select 绑定排除）/truncate/exists。
- 记录偏差：map 输入列序为排序序（Go map 无插入序），`query.Row` 可精确控制。

## R4 unions + joinSub

- OrderBy/Limit/Offset 在 union 后正确路由到 unionOrders/UnionLimit/UnionOffset（Laravel 语义）。
- golden：union/unionAll/union+order+limit/union aggregate（`from ((...) union (...)) as "temp_table"`）/joinSub/leftJoinSub。

## R5 date / json / fulltext where

- 日期家族：whereDate/Time/Day/Month/Year + Or 变体（base: `date(col)`；`extract` 留给 PG hook）。
- JSON 家族：whereJsonContains/DoesntContain/Overlaps/ContainsKey/Length + not/or 变体（MySQL: json_contains/json_overlaps/ifnull(json_contains_path)/json_length，路径 `'$."a"."b"[0]'`）；绑定 json_encode（`["en"]`）。
- fulltext：match against（natural/boolean/expanded 多列）。
- golden：23 条。

## R6 事务 + savepoint + afterCommit

- `Connection` 事务栈：BeginTransaction/Commit/RollBack/RollBackToLevel/TransactionLevel/Transaction(fn, attempts)。
- savepoint 语义对齐 Laravel：`SAVEPOINT trans{n+1}` / `ROLLBACK TO SAVEPOINT trans{n+1}`；仅最外层提交；死锁/锁等待自动重试（1213/1205/40001）。
- AfterCommit：无事务立即执行，事务内排队至最外层提交（TransactionsManager::addCallback 语义）。
- 事务内执行路由：Select/Scan/Insert/Update/Delete/Statement 走当前 `*sql.Tx`。
- 测试：commit/rollback/嵌套 savepoint/嵌套回滚/afterCommit 队列与立即执行/OnQuery 事件。

## R7 读取族

- Value/RawValue/Pluck（支持标量切片）/Implode/Sole（ErrNotFound/ErrMultiple）/FirstOrFail/FirstOr/Chunk（分块+提前终止）/Each。
- fake driver 执行测试全绿。

## R8 Postgres 方言

- `PostgresGrammar`：distinct on、`::date`/`::time`/`extract`、like 强制 `::text`、ilike、`::jsonb @>`、jsonb_array_length、jsonb_set 更新、`returning "id"`、`on conflict do nothing`、`on conflict (...) do update set ... = "excluded".`、for update/for share、truncate restart identity、to_tsvector 全文、JSON 选择器 `->>`/`->` 路径。
- golden：16 条 PG 断言。
- **修复自递归**：PG 构造器误把 compileRandomFn 指向继承方法导致无限递归——构造器只应注入"有差异"的钩子。

## R9 内省与提示

- ToRawSql（SubstituteBindingsIntoRawSql 移植：字符串字面量/`''`/`\'`/`??` 成对跳过、绑定转义内联）、Dump/DumpRawSql/DD、Explain（SelectMaps）、UseIndex/ForceIndex/IgnoreIndex（MySQL use/force/ignore index + 名称校验）、oldest/latest/reorder。
- golden：toRawSql（含引号转义 `it''s`）、index hints、lock（base 方言 lock 为空串=Laravel 基础行为）、reorder。

## R10 Model 层（Eloquent 对齐第一批）

- 模型元数据（表名/主键/时间戳/软删除探测）+ 软删除全局 scope（执行期最后追加，对齐 Laravel 全局 scope 时序）。
- Create：created_at/updated_at 自动填充 + 主键回填；自增主键为零值不参与写入；nil 指针字段不参与写入。
- Save：主键为空→插入，否则按主键更新（排除主键列、刷新 updated_at）。
- DeleteModel（软删除置 deleted_at / 硬删除）/ForceDelete/Restore（置 NULL）。
- FirstOrCreate/FirstOrNew/UpdateOrCreate。
- 测试 9 条（fake driver 验证 SQL 形态与生命周期行为）。

## R11 错误分类

- `*torm.Error{Kind, Err, Sql, Bindings}`：KindUniqueConstraint（1062/23505）/KindDeadlock（1213/1205/40001/40P01）/KindLostConnection（gone away/connection refused/broken pipe/0800x）；SQLSTATE 接口探测；Unwrap 链；四个执行入口统一包装。
- 测试：7 条分类 + 包装断言。

## R13 ChunkById / Lazy / Cursor（iter.Seq）+ 读写分离最小集

- `Cursor()`：流式行读取（`iter.Seq2[map[string]any, error]`，迭代结束/提前终止自动关闭语句）。
- `Lazy(perPage)`：limit/offset 翻页流式；`LazyById(perPage)`：id 游标翻页（无大 offset 问题）。
- `ChunkById(perPage, makeDest, callback)`：`ForPageAfterId`（where col > lastId + orderBy col asc + limit，移除该列既有排序）+ 游标自动推进；`EachById`。
- `UseWritePdo()`；`Config.ReadDsn` 只读连接：select 类走读、写语句恒走写、事务内走事务（fake driver 双 store 路由测试）。
- fake driver 桩改为顺序队列（同一 SQL 多次期望按序弹出）。

## R14 Schema Builder（P3）

- `grammar/schema.go`：Blueprint（ID/Increments/BigIncrements/String/Char/Text/Integer/BigInteger/Boolean/Date/DateTime/Timestamp/Decimal/JSON/UUID/ForeignID/Nullable/Default/DefaultExpression/Primary/StartingValue/Timestamps/SoftDeletes/RememberToken/Drop/DropIfExists）+ Index/Unique/Primary/Foreign（References/On/CascadeOnDelete/RestrictOnDelete/SetNullOnDelete）+ SchemaGrammar DDL 接口。
- MySQL 编译对齐 Laravel Schema 测试期望：`create table \`users\` (\`id\` int unsigned not null auto_increment primary key, \`email\` varchar(255) not null) default character set utf8 collate 'utf8_unicode_ci' engine = InnoDB`；alter 逐列 add；索引独立 alter；自增起始值独立语句；uuid 主键走尾部 primary key 段。
- Postgres：serial/bigserial、timestamp(0) without time zone、jsonb、`alter table add column`、`create [unique] index`、drop table if exists。
- SchemaBuilder：CreateTable/AlterTable/Drop/DropIfExists/HasTable/GetColumnListing（information_schema 内省）。

## R15 迁移器

- `Migrator`：注册式迁移（Name/Up/Down）；Migrate（建 migrations 表、跳过已运行、同批同 batch）；Rollback（最后一批逆序 Down + 删记录）；fake driver 全流程测试 4 条。

## R16-R17 Eloquent 关系（P4 第一批）

- `BelongsTo/HasOne/HasMany/BelongsToMany` 关系定义（键约定对齐 Laravel：belongsTo 外键默认取目标表单数_id，hasMany 外键默认取父表单数_id）。
- `With(name, rel)` 预加载：Get 后单次关联查询（whereIn 批量），反射填充结构体字段（StudlyCase 命名；HasMany 填 []T，HasOne/BelongsTo 填 *T）——无 N+1（测试断言查询次数）。
- `BelongsToMany` 经 pivot 单次 join 查询（`select roles.*, pivot.user_id as pivot_user_id from roles inner join role_user ... where ... in (...)`），pivot 表默认命名（两表单数字典序拼接）。
- `WhereHas/OrWhereHas`（where exists + 约束闭包，子查询绑定并入父查询）、`WithCount`（`(select count(*) ...) as "posts_count"` 子查询列）。

## R18 事件与 Observer

- `Observer` 结构（Retrieved/Saving/Saved/Creating/Created/Updating/Updated/Deleting/Deleted/Restoring/Restored/ForceDeleted），`Connection.Observe(model, o)` 按 reflect.Type 注册（并发安全）。
- 生命周期顺序对齐 Laravel：Create = saving→creating→created→saved；Save 更新 = saving→updating→updated→saved；DeleteModel/Destroy = deleting→deleted；ForceDelete = deleting→deleted→forceDeleted；Restore = restoring→restored。
- 回调返回 error 中断生命周期（SQL 不执行）；`Retrieved` 在 Get 后逐模型触发（按 dest 元素类型查找观察者）。
- 修复周期 25：Retrieved 按 *[]T 类型查找失败 → 增加 observerForType 按元素类型匹配。

## R19 casts（json）

- 字段标签 `torm:"cast:json"`：写入时结构体/切片经 json.Marshal 序列化为字符串进绑定；读取时列字符串扫入占位符后 json.Unmarshal 回填字段（Field.ReadCast/WriteCast + scan 的 castHolders 路径）。
- 修复周期 26：多 json 字段的占位符需映射到具体 Field（初版重定位逻辑会串值）。
- 修复周期 27：指针模型 `reflect.TypeOf(&T{}).Name()` 返回空串导致表名为空 → inspectModel 用 reflect.Indirect 取类型（历史 Model() 同源问题一并修复）。

## R20 SqlServer 方言钩子子集

- `SqlServerGrammar`：`[标识符]` wrap（`]` 转义）、`output inserted.[id]` insertGetId、空值 insert `default values`、随机 `NEWID()`、lock `with (rowlock,updlock,holdlock)`、year/month/day/date 时间函数。
- golden 7 条。修复周期 28：output 子句误插到列列表内 → 按 " values " 分段插入。

## R21 HasManyThrough

- `HasManyThrough{ThroughModel, FirstKey, SecondKey, LocalKey, SecondLocalKey, TargetModel}`：经中间表单次 join 预加载（`select comments.*, posts.user_id as laravel_through_key from comments inner join posts ... where posts.user_id in (...)`），按 through key 分组回填。
- 默认键约定：FirstKey = 父表单数_id；SecondKey = 中间表单数_id。

## R22 Morph 系列（morphMany / morphOne / morphTo）

- `MorphMany/MorphOne{MorphName, MorphKey, TypeColumn, IDColumn, TargetModel}`：预加载 `select * from target where type_col = ? and id_col in (...)`，按 id_col 分组回填（MorphOne 取首个）。
- `MorphTo{MorphName, TypeColumn, IDColumn, TypeMap}`：反向多态，按类型列分桶、逐类型 whereIn 查询、回填 interface{} 字段。
- 类型串约定：MorphKey 缺省 = 父表名（Go 侧无 FQCN；Laravel 默认 FQCN，可用 morph map 等价替代）。
- 修复周期 30：多态列名误从 With 字段名推导 → 增加 MorphName 字段（morphName_type / morphName_id）。
- 修复周期 31：局部类型缺 TableName 导致表名推导错误（测试改包级类型）。

## R23 集合层

- 泛型 `Collection[T]`：Col/Slice/Len/Each（false 停止）/Filter/Map/First/Contains/Pluck（Go 字段名或 torm 列名）/ModelKeys（主键集合，对齐 Eloquent Collection::modelKeys）。
- 修复周期 32：不可寻址元素的 Addr panic → 复制到可写副本再建 Schema。

## R24 factory / seeder

- `Factory[T].Make/Create`：Create 走完整 Create 生命周期（时间戳/观察者/主键回填）；`Seed(conn, seeders...)` 顺序执行、失败中止。
- 修复周期 33：——（一次通过）。

## R25 SqlServer limit/offset 重写 + MariaDB 方言

- SqlServer：limit 无 offset → `select top(n) ...`；offset → `offset N rows`；二者同现 → `offset N rows fetch next M rows only`。组件顺序经 `SetSelectComponents` 覆盖（offset 先于 limit，对齐 Laravel SqlServerGrammar 的 selectComponents）。
- `MariaDbGrammar`：MySQL 编译行为子集一致，独立类型支撑方言注册与后续差异扩展。
- 修复周期 34：SqlServer 构造器钩子误绑提升方法（compileColumns-fm 自递归栈溢出）→ 补齐方言自身方法；组件顺序缺省导致 offset/limit 次序错误 → SetSelectComponents 覆盖。

## R26 MorphToMany / morphedByMany

- `MorphToMany{RelatedModel, MorphName, MorphKey, Table, TypeColumn, ForeignPivotKey, RelatedPivotKey, RelatedKey}` 与 `MorphedByMany`（别名类型，共用 pivot 机制）。
- 预加载：`select tags.*, taggables.taggable_id as pivot_taggable_id from tags inner join taggables on tags.id = taggables.tag_id where taggables.taggable_type = ? and taggables.taggable_id in (...)`；pivot 表缺省 = plural(morphName)（taggable → taggables）。
- 修复周期 30-31 补记：MorphName 显式传递（morphToMany($name) 对应）。

## R27 groupLimit

- `query.Query.GroupLimit{Column, Value}` + `compileGroupLimit` / `compileRowNumber` 移植：`select * from (select "posts".*, row_number() over (partition by "user_id" order by "created_at" desc) as "laravel_row" from "posts") as "laravel_table" where "laravel_row" <= 2 order by "laravel_row"`；带 offset 时 `laravel_row <= limit+offset and laravel_row > offset`。
- 修复周期 32：compileComponents 重构为带名组件（namedComponent），groupLimit 需定位/剔除 orders 组件并把 row_number 追加到 columns。

## R28 事件分发器体系

- `Event`/`ModelEvent` 事件对象、`Dispatcher` 接口、`EventBus`（按事件名注册 Listener）。
- `Connection.SetEventDispatcher`：生命周期点同步分发同名事件对象（saving/creating/created/saved/updating/updated/deleting/deleted/restoring/restored/forceDeleted/retrieved），与 Observer 钩子共存（钩子先、事件后，钩子错误中断不分发）。
- 生命周期路径统一收敛到 `fireModelEvent(ob, name, model)`（hook 判空 + 分发一体）。
- 修复周期 33-34：分发调用误置于观察者守卫内（无观察者时事件不触发）→ 统一无条件分发；fireRetrieved 重构时误删 fireModelEvent（编译暴露）→ 补回。

## R29 MorphMap + With 预加载 limit 自动接入 GroupLimit

- **MorphMap**：`SetMorphMap(map[string]any)`（全局注册，别名→模型；对齐 Laravel Relation::enforceMorphMap）、`MorphModelForAlias` / `MorphAliasOf`（反查）、`ResetMorphMap`。
  - MorphMany/MorphOne 的类型列取值优先级：显式 MorphKey > morph map 别名（按父模型类型反查）> 父表名。
  - MorphTo：TypeMap 未命中时自动经全局 morph map 解析目标模型。
- **With 预加载 limit 自动接入 GroupLimit**：`WithConstraints(name, rel, fns...)` 与 `WithLimit(name, rel, limit, fns...)`；约束闭包或显式 limit 设置后，加载查询自动编译为 row_number 分组限额形态：
  `select * from (select \`posts\`.*, row_number() over (partition by \`user_id\` order by \`created_at\` desc) as \`laravel_row\` from \`posts\` where \`user_id\` in (?, ?)) as \`laravel_table\` where \`laravel_row\` <= 2 order by \`laravel_row\``
- 修复周期 35：morphKey 别名解析未生效（morphKeyForParent 未接线）→ 接线；36：WithLimit 约束闭包需先 Select("posts.*") 才能得到 Laravel 经典 group-limit SQL 形态（nil 列缺省 `select *` 与 Laravel 编译层一致）。

## 未尽事项（诚实清单，截至 R29）

- 集合层：泛型常用子集（高阶消息代理、lazy collection 惰性语义按 Go iter 习惯部分映射）。
- factory/seeder：精简形态（无 Laravel 的状态重置/类回调体系）。
- MorphToMany 的 morph map 类型串已支持；morph map 缺省 FQCN 语义不适用 Go（无类全名惯例，以别名/表名为约定）。
- 已知偏差：map 输入的 insert/update 列序为排序序（Go map 无序；Row 保序可精确对齐）；动态 where 等 PHP 魔法特性按 ⛔ 设计边界不移植。

## 附：逐轮「检查 → 修正」记录（24 个独立修复周期，全部以 go build/vet/test 回归验证）

## 附：逐轮「检查 → 修正」记录（24 个独立修复周期，全部以 go build/vet/test 回归验证）

| # | 轮 | 发现（检查手段） | 修正 |
|---|---|---|---|
| 1 | 升级 | go vet 报 table_test unreachable | panic+t.Error → t.Fatal |
| 2 | R1 | fakeStmt 未实现 driver.Stmt 接口（编译） | Exec/Query 改 legacy []driver.Value |
| 3 | R1 | []*User 多行 panic（fake 测试） | scan 循环每行分配新元素 |
| 4 | R1 | struct dest 扫描后零值（fake 测试） | struct 分支直接扫入 dest |
| 5 | R1 | TestTableSelectOne 无守卫 panic（签名 test *testing.T 漏改） | 统一签名+补 skipNoDB |
| 6 | R2 | BaseGrammar 调不到方言 wrapValue（编译） | 弃虚接口改钩子注入，重构两遍 |
| 7 | R2 | joinClauseQuery 传参类型错（编译） | 修 join 包装函数 |
| 8 | R2 | CompileRandom 未导出不满足接口（编译） | 增加导出方法 |
| 9 | R2 | Where(id,1) 绑定成 "1"（golden bindings） | parseWhereArgs 单参=值 |
| 10 | R2 | addSelect 重复列/切片未展开（探针） | 列表展开+去重（Laravel 13 语义） |
| 11 | R2 | MySQL 引号转义用例输入写错（golden） | 拆分双方言各自输入 |
| 12 | R2 | 聚合 golden 缺 From / distinct(*) 期望错（golden） | 修用例；补 distinct-on-列语义 |
| 13 | R2 | 子查询回调 func(*Builder)*Builder 不识别（探针 `() as "post_id"`） | createSub/isQueryable 兼容三种形态 |
| 14 | R2 | 嵌套 where 绑定丢失（golden bindings） | addNestedWhereQuery 并入子 where 绑定（查 Laravel 源码确认） |
| 15 | R2 | whereSub 双层括号（golden） | Sub 直接持子 Query，去掉包装 |
| 16 | R3 | 插入列序无法复现 PHP 插入序（exec golden） | 引入 query.Row 有序行类型 |
| 17 | R3 | exec 断言 int vs int64（fake 测试） | database/sql 转换语义对齐 |
| 18 | R3 | InsertUsing 丢弃子查询绑定（exec golden） | createSub 绑定并入 where 段 |
| 19 | R4 | union 后 order/limit 进了主查询（golden） | OrderBy/Limit/Offset 路由 unionOrders/UnionLimit |
| 20 | R5 | —（date/json/fulltext 23 条一次通过） | 仅回归 |
| 21 | R6 | COMMIT 未被记录/afterCommit 时机错（事务测试） | fake tx 记录；hook 移入事务内 |
| 22 | R8 | PostgresGrammar.CompileRandom 无限递归（栈溢出） | 构造器只注入有差异的钩子 |
| 23 | R8 | PG JSON path 引号错（golden） | 属性单引号/数组下标裸数字 |
| 24 | R10 | Model CRUD 误带软删除 scope、insert 混入零值主键与 nil 字段（fake 测试日志） | scope 执行期后置；Attributes() 跳过空白主键与 nil 指针；Save 排除主键列 |
| 25 | R18 | Retrieved 按 *[]T 查找观察者失败（测试） | observerForType 按元素类型匹配 |
| 26 | R19 | 多 json 字段占位符串值（代码审读） | castHolders 直接映射 scan 下标 → Field |
| 27 | R19 | 指针模型表名为空（cast 测试） | inspectModel 用 reflect.Indirect 取类型名 |
| 28 | R20 | output 子句插入位置错（golden） | 按 " values " 分段插入 |
| 29 | R21 | ThroughModel 未实现 Relation 接口（编译） | 表名解析改走 inspectModel |
