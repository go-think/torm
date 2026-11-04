package torm

import (
	"errors"
	"reflect"
	"time"

	"github.com/thinkoner/torm/field"
	"github.com/thinkoner/torm/utils"
)

// Model 是内嵌属性集合（兼容旧用法）。
type Model struct {
	field.IDAttr
	field.CreatedAtAttr
	field.UpdatedAtAttr
}

// SoftDeletes 是软删除内嵌属性（兼容旧用法）。
type SoftDeletes struct {
	field.DeletedAtAttr
}

// modelInfo 模型元数据（对应 Laravel Model 的 $table/$primaryKey/$timestamps 等）。
type modelInfo struct {
	table          string
	primaryKey     string
	hasTimestamps  bool
	softDeletes    bool
	deletedAtField string
}

// inspectModel 反射解析模型元数据。
func inspectModel(model interface{}) *modelInfo {
	info := &modelInfo{primaryKey: "id"}

	kind := reflect.Indirect(reflect.ValueOf(model)).Kind()
	if kind != reflect.Struct {
		panic("unsupported model, should be slice or struct")
	}

	val := reflect.Indirect(reflect.ValueOf(model))
	modelType := val.Type()

	if t, ok := model.(TableName); ok {
		info.table = t.TableName()
	} else if t, ok := reflect.New(modelType).Interface().(TableName); ok {
		info.table = t.TableName()
	} else {
		info.table = utils.SnakeCase(modelType.Name())
	}

	schema, err := NewSchema(model)
	if err == nil {
		for _, f := range schema.Fields {
			switch f.Name {
			case "created_at", "updated_at":
				info.hasTimestamps = true
			case "deleted_at":
				info.softDeletes = true
				info.deletedAtField = f.Name
			}
		}
		for _, f := range schema.Fields {
			if f.Primary {
				info.primaryKey = f.Name
				break
			}
		}
	}

	return info
}

// setModelAttrs 按列名把值写入模型字段。
func setModelAttrs(model interface{}, attrs map[string]interface{}) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	for _, f := range schema.Fields {
		if v, ok := attrs[f.Name]; ok {
			if err := f.SetValue(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// nowFunc 可测试的时间源。
var nowFunc = time.Now

// applyTimestamps 填充 created_at / updated_at。
func applyTimestamps(model interface{}) {
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	now := nowFunc()
	for _, f := range schema.Fields {
		switch f.Name {
		case "created_at", "updated_at":
			if f.Value.Kind() == reflect.Struct && f.Value.CanSet() {
				if t, ok := f.Value.Interface().(time.Time); ok && t.IsZero() {
					f.Value.Set(reflect.ValueOf(now))
				} else if ok && f.Name == "updated_at" {
					f.Value.Set(reflect.ValueOf(now))
				}
			}
		}
	}
}

// touchUpdatedAt 仅刷新 updated_at。
func touchUpdatedAt(model interface{}) {
	schema, err := NewSchema(model)
	if err != nil {
		return
	}
	now := nowFunc()
	for _, f := range schema.Fields {
		if f.Name == "updated_at" && f.Value.CanSet() {
			f.Value.Set(reflect.ValueOf(now))
		}
	}
}

// Model Begin a fluent query against a database Model.
// 含软删除全局 scope（deleted_at IS NULL）。
func (c *Connection) Model(model interface{}) *Builder {
	info := inspectModel(model)
	b := c.Table(info.table)
	b.modelInfo = info
	return b
}

// Create Save a new model to the database（含时间戳填充与主键回填）。
func (c *Connection) Create(model interface{}) error {
	applyTimestamps(model)

	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	attributes := schema.Attributes()

	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventSaving, model); err != nil {
		return err
	}
	if err := c.fireModelEvent(ob, EventCreating, model); err != nil {
		return err
	}

	insertId, _, err := c.Model(model).Insert(attributes)
	if err != nil {
		return err
	}

	if err := schema.SetId(insertId); err != nil {
		return err
	}

	if err := c.fireModelEvent(ob, EventCreated, model); err != nil {
		return err
	}
	if err := c.fireModelEvent(ob, EventSaved, model); err != nil {
		return err
	}

	return nil
}

// Save Save the model to the database：主键为空 → 插入；否则更新（含时间戳）。
func (c *Connection) Save(model interface{}) error {
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}

	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return c.Create(model)
	}

	info := inspectModel(model)

	touchUpdatedAt(model)
	attributes := schema.Attributes()
	if schema.PrimaryField != nil {
		delete(attributes, schema.PrimaryField.Name)
	}

	pk := info.primaryKey
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventSaving, model); err != nil {
		return err
	}
	if err := c.fireModelEvent(ob, EventUpdating, model); err != nil {
		return err
	}

	_, err = c.Table(info.table).
		Where(pk, schema.PrimaryField.Value.Addr().Interface()).
		Update(attributes)
	if err != nil {
		return err
	}

	if err := c.fireModelEvent(ob, EventUpdated, model); err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventSaved, model)
}

// Destroy 按主键硬删除模型行。
func (c *Connection) Destroy(model interface{}) error {
	info := inspectModel(model)
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventDeleting, model); err != nil {
		return err
	}
	if schema.PrimaryField != nil && !schema.PrimaryField.IsBlank {
		_, err = c.Table(info.table).
			Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
			Delete()
	} else {
		_, err = c.Table(info.table).Delete()
	}
	if err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventDeleted, model)
}

// DeleteModel 删除模型：软删除模型置 deleted_at，否则硬删除
// （命名避开 Connection.Delete 的原生 SQL 方法；对应 Laravel 模型 delete()）。
func (c *Connection) DeleteModel(model interface{}) error {
	info := inspectModel(model)
	if !info.softDeletes {
		return c.Destroy(model)
	}
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("cannot delete model without primary key")
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventDeleting, model); err != nil {
		return err
	}
	_, err = c.Table(info.table).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
		Update(map[string]interface{}{info.deletedAtField: nowFunc()})
	if err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventDeleted, model)
}

// ForceDelete 绕过软删除硬删除。
func (c *Connection) ForceDelete(model interface{}) error {
	info := inspectModel(model)
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	ob := observerFor(model)
	if err := c.fireModelEvent(ob, EventDeleting, model); err != nil {
		return err
	}
	if schema.PrimaryField != nil && !schema.PrimaryField.IsBlank {
		_, err = c.Table(info.table).
			Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
			Delete()
	} else {
		_, err = c.Table(info.table).Delete()
	}
	if err != nil {
		return err
	}
	if err := c.fireModelEvent(ob, EventDeleted, model); err != nil {
		return err
	}
	return c.fireModelEvent(ob, EventForceDeleted, model)
}

// Restore 恢复软删除的模型。
func (c *Connection) Restore(model interface{}) error {
	info := inspectModel(model)
	if !info.softDeletes {
		return nil
	}
	schema, err := NewSchema(model)
	if err != nil {
		return err
	}
	if schema.PrimaryField == nil || schema.PrimaryField.IsBlank {
		return errors.New("cannot restore model without primary key")
	}
	_, err = c.modelTableWithoutSoftDeletes(info).
		Where(info.primaryKey, schema.PrimaryField.Value.Addr().Interface()).
		Update(map[string]interface{}{info.deletedAtField: nil})
	return err
}

// modelTableWithoutSoftDeletes 恢复/强制操作需要带 deleted_at 条件的查询。
func (c *Connection) modelTableWithoutSoftDeletes(info *modelInfo) *Builder {
	return c.Table(info.table)
}

// FirstOrCreate 按 attributes 查找；未命中则合并 attributes 创建模型（对应 Laravel firstOrCreate）。
func (c *Connection) FirstOrCreate(model interface{}, attributes map[string]interface{}) error {
	b := c.Model(model)
	for _, k := range sortedColumnNames(attributes) {
		b.Where(k, attributes[k])
	}
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		if err := setModelAttrs(model, attributes); err != nil {
			return err
		}
		return c.Create(model)
	}
	return nil
}

// FirstOrNew 按 attributes 查找；未命中仅构造不落库。
func (c *Connection) FirstOrNew(model interface{}, attributes map[string]interface{}) error {
	b := c.Model(model)
	for _, k := range sortedColumnNames(attributes) {
		b.Where(k, attributes[k])
	}
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		return setModelAttrs(model, attributes)
	}
	return nil
}

// UpdateOrCreate 按 attributes 查找；命中则更新 values，未命中则创建（对应 Laravel updateOrCreate）。
func (c *Connection) UpdateOrCreate(model interface{}, attributes, values map[string]interface{}) error {
	b := c.Model(model)
	for _, k := range sortedColumnNames(attributes) {
		b.Where(k, attributes[k])
	}
	if err := b.First(model); err != nil {
		return err
	}
	if isEmptyDest(model) {
		merged := make(map[string]interface{}, len(attributes)+len(values))
		for k, v := range attributes {
			merged[k] = v
		}
		for k, v := range values {
			merged[k] = v
		}
		if err := setModelAttrs(model, merged); err != nil {
			return err
		}
		return c.Create(model)
	}
	if err := setModelAttrs(model, values); err != nil {
		return err
	}
	return c.Save(model)
}
