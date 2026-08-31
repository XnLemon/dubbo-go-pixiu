<!--
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 -->
<template>
  <CustomLayout>
    <div class="custom-body route-binding-page">
      <CommonTitle title="路由模型管理"></CommonTitle>

      <div class="intro-panel">
        <div>
          <span class="intro-title">AdminRouteBinding</span>
          <span class="intro-copy">
            用高层对象描述 HTTP 到 Dubbo 的调用，保存草稿不会影响运行中的 Pixiu。
          </span>
        </div>
        <div class="intro-actions">
          <el-tag v-if="schemaReady" type="success" size="mini">Schema 已加载</el-tag>
          <el-tag v-else type="info" size="mini">Schema 加载中</el-tag>
          <el-button size="mini" icon="el-icon-refresh" @click="refreshAll">刷新</el-button>
          <el-button type="primary" size="mini" icon="el-icon-plus" @click="newBinding">
            新建草稿
          </el-button>
        </div>
      </div>

      <el-row :gutter="12" class="binding-layout">
        <el-col :span="8">
          <div class="panel binding-list-panel">
            <div class="panel-header">
              <span>路由绑定列表</span>
              <span class="muted">{{ bindings.length }} 条</span>
            </div>
            <el-table
              v-loading="listLoading"
              :data="bindings"
              size="mini"
              stripe
              highlight-current-row
              empty-text="暂无草稿或已发布配置"
              @row-click="selectBinding"
            >
              <el-table-column prop="name" label="名称" min-width="118">
                <template slot-scope="scope">
                  <span class="binding-name">{{ scope.row.name }}</span>
                </template>
              </el-table-column>
              <el-table-column label="状态" width="78">
                <template slot-scope="scope">
                  <el-tag :type="statusType(scope.row.status)" size="mini">
                    {{ statusLabel(scope.row.status) }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="版本" width="76">
                <template slot-scope="scope">
                  <span>{{ revisionLabel(scope.row) }}</span>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-col>

        <el-col :span="16">
          <div class="panel editor-panel">
            <div class="panel-header editor-header">
              <div>
                <span>{{ currentName || '未命名草稿' }}</span>
                <el-tag v-if="editorDirty" type="warning" size="mini" class="dirty-tag">
                  未保存
                </el-tag>
                <span v-if="currentRecord" class="record-meta">
                  resource {{ currentRecord.resourceId }} / method {{ currentRecord.methodId }}
                </span>
              </div>
              <div class="editor-actions">
                <el-button size="mini" @click="loadCurrent" :disabled="!currentName">
                  重新载入
                </el-button>
                <el-button size="mini" @click="openHistory" :disabled="!currentName">
                  历史版本
                </el-button>
                <el-button size="mini" @click="previewBinding" :disabled="!editorContent.trim()">
                  预览 / Diff
                </el-button>
                <el-button type="primary" size="mini" @click="saveDraft()" :loading="saving">
                  保存草稿
                </el-button>
                <el-button
                  type="success"
                  size="mini"
                  @click="publishBinding"
                  :loading="publishing"
                  :disabled="saving"
                >
                  发布
                </el-button>
                <el-button
                  v-if="currentName"
                  type="danger"
                  plain
                  size="mini"
                  @click="deleteDraft"
                >
                  删除草稿
                </el-button>
              </div>
            </div>
            <div class="editor-hint">
              编辑器接受 YAML 或 JSON。服务端会按注册表补齐默认值并执行字段、路由和参数映射校验。
            </div>
            <el-input
              v-model="editorContent"
              class="route-editor"
              type="textarea"
              :rows="25"
              resize="none"
              spellcheck="false"
              placeholder="粘贴 AdminRouteBinding YAML 或 JSON"
            ></el-input>
          </div>

          <div v-if="publishedProjection" class="panel projection-panel">
            <div class="panel-header">
              <span>当前已发布 Projection</span>
              <span class="muted">运行时读取 legacy Resource / Method</span>
            </div>
            <pre class="code-block">{{ publishedProjection }}</pre>
          </div>
        </el-col>
      </el-row>
    </div>

    <el-dialog title="预览 / Diff" :visible.sync="previewVisible" width="82%">
      <div v-if="previewResult" class="preview-meta">
        <span>Resource ID: {{ previewResult.resourceId || '-' }}</span>
        <span>Method ID: {{ previewResult.methodId || '-' }}</span>
        <span v-if="previewResult.publishedRevision">
          已发布版本: {{ previewResult.publishedRevision }}
        </span>
      </div>
      <el-tabs v-model="previewTab">
        <el-tab-pane label="生成的 legacy YAML" name="yaml">
          <pre class="code-block dialog-code">{{ previewResult.legacyYaml || '暂无内容' }}</pre>
        </el-tab-pane>
        <el-tab-pane label="与已发布版本的 Diff" name="diff">
          <pre class="code-block dialog-code">{{ previewResult.diff || '暂无差异（尚无已发布版本或内容未变化）' }}</pre>
        </el-tab-pane>
      </el-tabs>
    </el-dialog>

    <el-dialog title="发布历史" :visible.sync="historyVisible" width="76%">
      <el-table v-loading="historyLoading" :data="history" size="mini" empty-text="暂无发布历史">
        <el-table-column prop="revision" label="版本" width="90"></el-table-column>
        <el-table-column label="入口" min-width="180">
          <template slot-scope="scope">{{ historyEntry(scope.row) }}</template>
        </el-table-column>
        <el-table-column label="Dubbo 方法" min-width="180">
          <template slot-scope="scope">{{ historyTarget(scope.row) }}</template>
        </el-table-column>
        <el-table-column prop="updatedAt" label="发布时间" width="190">
          <template slot-scope="scope">{{ formatTime(scope.row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="110">
          <template slot-scope="scope">
            <el-button
              type="text"
              :disabled="!canRollback(scope.row)"
              @click="rollback(scope.row)"
            >
              {{ canRollback(scope.row) ? '回滚到此版本' : '当前版本' }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>
  </CustomLayout>
</template>

<script>
import CommonTitle from '@/components/common/CommonTitle'
import CustomLayout from '@/components/common/CustomLayout.vue'

const ROUTE_BINDING_BASE = '/config/api/route-binding'

const SAMPLE_ROUTE_BINDING = `kind: AdminRouteBinding
metadata:
  name: route-example
spec:
  entry:
    protocol: http
    path: /api/v1/users/:id
    method: GET
  target:
    protocol: dubbo
    application: UserProvider
    interface: com.example.UserService
    method: GetUser
    version: 1.0.0
    group: stable
    cluster: user-dubbo
  params:
    - from: uri.id
      to: 0
      type: java.lang.String
  timeout: 2s
  publish:
    mode: draft
    validate: true
`

export default {
  name: 'RouteBinding',
  components: {
    CommonTitle,
    CustomLayout
  },
  data () {
    return {
      bindings: [],
      currentName: '',
      currentDetail: null,
      selectedBinding: null,
      editorContent: SAMPLE_ROUTE_BINDING,
      editorSnapshot: SAMPLE_ROUTE_BINDING,
      schemaReady: false,
      schemaFieldCount: 0,
      listLoading: false,
      saving: false,
      publishing: false,
      historyLoading: false,
      historyVisible: false,
      history: [],
      previewVisible: false,
      previewTab: 'yaml',
      previewResult: null
    }
  },
  computed: {
    currentRecord () {
      if (!this.currentDetail) {
        return null
      }
      return this.currentDetail.draft || this.currentDetail.published || null
    },
    publishedProjection () {
      if (!this.currentDetail || !this.currentDetail.published) {
        return ''
      }
      return this.currentDetail.published.legacyYaml || ''
    },
    editorDirty () {
      return this.editorContent !== this.editorSnapshot
    }
  },
  mounted () {
    this.refreshAll()
  },
  methods: {
    refreshAll () {
      this.loadSchema()
      this.loadList()
    },
    loadSchema () {
      return this.$get(ROUTE_BINDING_BASE + '/schema')
        .then((res) => {
          if (!this.isOK(res)) {
            this.showError(res, '加载 Schema 失败')
            return
          }
          const schemas = Array.isArray(res.data) ? res.data : []
          const routeSchema = schemas.find(item => item.kind === 'AdminRouteBinding')
          this.schemaReady = !!routeSchema
          this.schemaFieldCount = routeSchema && routeSchema.fields
            ? Object.keys(routeSchema.fields).length
            : 0
        })
        .catch(() => {
          this.schemaReady = false
          this.$message.error('加载 Schema 失败，请检查 Admin 服务')
        })
    },
    loadList () {
      this.listLoading = true
      return this.$get(ROUTE_BINDING_BASE + '/list')
        .then((res) => {
          if (!this.isOK(res)) {
            this.showError(res, '加载路由绑定列表失败')
            this.bindings = []
            return
          }
          this.bindings = Array.isArray(res.data) ? res.data : []
          if (this.currentName) {
            const current = this.bindings.find(item => item.name === this.currentName)
            if (current) {
              this.selectedBinding = current
            }
          }
        })
        .catch(() => {
          this.bindings = []
          this.$message.error('加载路由绑定列表失败，请稍后重试')
        })
        .then(() => {
          this.listLoading = false
        })
    },
    selectBinding (row) {
      if (!row || !row.name) {
        return
      }
      this.selectedBinding = row
      this.currentName = row.name
      this.loadDetail(row.name, true)
    },
    loadCurrent () {
      if (this.currentName) {
        this.loadDetail(this.currentName, true)
      }
    },
    loadDetail (name, replaceEditor) {
      return this.$get(ROUTE_BINDING_BASE + '/detail', { name })
        .then((res) => {
          if (!this.isOK(res)) {
            this.showError(res, '加载路由绑定详情失败')
            return
          }
          this.currentDetail = res.data || null
          if (this.currentDetail && this.currentDetail.name) {
            this.currentName = this.currentDetail.name
          }
          const record = this.currentRecord
          if (replaceEditor && record && record.object) {
            this.setEditorContent(this.objectToJSON(record.object))
          }
        })
        .catch(() => {
          this.$message.error('加载路由绑定详情失败，请稍后重试')
        })
    },
    newBinding () {
      this.currentName = ''
      this.currentDetail = null
      this.selectedBinding = null
      this.history = []
      this.setEditorContent(SAMPLE_ROUTE_BINDING)
      this.previewResult = null
      this.$message.info('请先修改 metadata.name，再保存草稿')
    },
    saveDraft (showMessage) {
      if (typeof showMessage === 'undefined') {
        showMessage = true
      }
      if (!this.editorContent || !this.editorContent.trim()) {
        this.$message.warning('配置内容不能为空')
        return Promise.resolve(null)
      }
      const formData = new FormData()
      formData.append('content', this.editorContent)
      this.saving = true
      return this.$post(ROUTE_BINDING_BASE, formData)
        .then((res) => {
          if (!this.isOK(res)) {
            this.showError(res, '保存草稿失败')
            return null
          }
          const record = res.data || null
          const object = record && record.object
          const metadata = object && object.metadata
          if (metadata && metadata.name) {
            this.currentName = metadata.name
          }
          // The server has accepted this exact editor text as the draft. Keep
          // the user's YAML formatting while clearing the local dirty state.
          this.editorSnapshot = this.editorContent
          if (showMessage) {
            this.$message.success('草稿已保存，尚未影响运行时')
          }
          if (this.currentName) {
            this.loadDetail(this.currentName, false)
          }
          this.loadList()
          return record
        })
        .catch(() => {
          this.$message.error('保存草稿失败，请检查网络或配置内容')
          return null
        })
        .then((record) => {
          this.saving = false
          return record
        })
    },
    previewBinding () {
      if (!this.editorContent || !this.editorContent.trim()) {
        this.$message.warning('配置内容不能为空')
        return
      }
      const formData = new FormData()
      formData.append('content', this.editorContent)
      this.$post(ROUTE_BINDING_BASE + '/preview', formData)
        .then((res) => {
          if (!this.isOK(res)) {
            this.showError(res, '生成预览失败')
            return
          }
          this.previewResult = res.data || {}
          this.previewTab = this.previewResult.diff ? 'diff' : 'yaml'
          this.previewVisible = true
        })
        .catch(() => {
          this.$message.error('生成预览失败，请检查配置内容')
        })
    },
    publishBinding () {
      // Publish always operates on a persisted draft. This also covers the
      // published-only state after a draft was discarded and prevents an
      // unsaved editor change from being silently ignored.
      const hasSavedDraft = !!(this.currentDetail && this.currentDetail.draft)
      if (!this.currentName || this.editorDirty || !hasSavedDraft) {
        this.saveDraft(false).then((record) => {
          const object = record && record.object
          const metadata = object && object.metadata
          if (!metadata || !metadata.name) {
            return
          }
          this.currentName = metadata.name
          this.confirmPublish(metadata.name)
        })
        return
      }
      this.confirmPublish(this.currentName)
    },
    confirmPublish (name) {
      this.$confirm(
        '发布后会写入 legacy resources/method keys，并由 Pixiu 运行时 watcher 生效，确定继续吗？',
        '确认发布',
        { type: 'warning' }
      ).then(() => {
        const formData = new FormData()
        formData.append('name', name)
        this.publishing = true
        return this.$put(ROUTE_BINDING_BASE + '/publish', formData)
          .then((res) => {
            if (!this.isOK(res)) {
              this.showError(res, '发布失败')
              return
            }
            this.$message.success('发布成功，Pixiu 将通过现有 watcher 更新')
            this.loadDetail(name, true)
            this.loadList()
            if (this.historyVisible) {
              this.loadHistory()
            }
          })
          .catch(() => {
            this.$message.error('发布失败，请稍后重试')
          })
          .then(() => {
            this.publishing = false
          })
      }).catch(() => {})
    },
    openHistory () {
      if (!this.currentName) {
        this.$message.warning('请先选择或保存一个路由绑定')
        return
      }
      this.historyVisible = true
      this.loadHistory()
    },
    loadHistory () {
      if (!this.currentName) {
        return Promise.resolve()
      }
      this.historyLoading = true
      return this.$get(ROUTE_BINDING_BASE + '/history', { name: this.currentName })
        .then((res) => {
          if (!this.isOK(res)) {
            this.showError(res, '加载发布历史失败')
            this.history = []
            return
          }
          this.history = Array.isArray(res.data) ? res.data : []
        })
        .catch(() => {
          this.history = []
          this.$message.error('加载发布历史失败，请稍后重试')
        })
        .then(() => {
          this.historyLoading = false
        })
    },
    rollback (record) {
      if (!record || !this.currentName) {
        return
      }
      this.$confirm(
        '回滚会生成一个新的发布版本并同步更新草稿，确定继续吗？',
        '确认回滚',
        { type: 'warning' }
      ).then(() => {
        const formData = new FormData()
        formData.append('name', this.currentName)
        formData.append('revision', String(record.revision))
        return this.$post(ROUTE_BINDING_BASE + '/rollback', formData)
          .then((res) => {
            if (!this.isOK(res)) {
              this.showError(res, '回滚失败')
              return
            }
            this.$message.success('回滚成功，已生成新的发布版本')
            this.loadDetail(this.currentName, true)
            this.loadList()
            this.loadHistory()
          })
          .catch(() => {
            this.$message.error('回滚失败，请稍后重试')
          })
      }).catch(() => {})
    },
    canRollback (record) {
      const published = this.currentDetail && this.currentDetail.published
      return !!record && !!published && record.revision < published.revision
    },
    deleteDraft () {
      if (!this.currentName) {
        return
      }
      this.$confirm(
        '只删除未发布草稿；如果已有发布版本，运行时 projection 会保留，确定继续吗？',
        '确认删除草稿',
        { type: 'warning' }
      ).then(() => {
        return this.$delete(ROUTE_BINDING_BASE, { name: this.currentName })
          .then((res) => {
            if (!this.isOK(res)) {
              this.showError(res, '删除草稿失败')
              return
            }
            this.$message.success('草稿已删除')
            const oldDetail = this.currentDetail
            const published = oldDetail && oldDetail.published
            if (published && published.object) {
              this.setEditorContent(this.objectToJSON(published.object))
            } else {
              this.setEditorContent(SAMPLE_ROUTE_BINDING)
              this.currentName = ''
              this.currentDetail = null
              this.history = []
            }
            if (this.currentName) {
              this.loadDetail(this.currentName, false)
            }
            this.loadList()
          })
          .catch(() => {
            this.$message.error('删除草稿失败，请稍后重试')
          })
      }).catch(() => {})
    },
    objectToJSON (object) {
      try {
        return JSON.stringify(object, null, 2)
      } catch (err) {
        return ''
      }
    },
    setEditorContent (content) {
      this.editorContent = content
      this.editorSnapshot = content
    },
    isOK (res) {
      return !!res && String(res.code) === '10001'
    },
    showError (res, fallback) {
      let message = fallback
      if (res && typeof res.data === 'string' && res.data) {
        message = res.data
      } else if (res && res.data && res.data.message) {
        message = res.data.message
      }
      this.$message.error(message)
    },
    statusLabel (status) {
      return {
        draft: '草稿',
        modified: '已修改',
        published: '已发布'
      }[status] || status || '未知'
    },
    statusType (status) {
      return {
        draft: 'info',
        modified: 'warning',
        published: 'success'
      }[status] || 'info'
    },
    revisionLabel (row) {
      if (!row) {
        return '-'
      }
      if (row.draftRevision && row.publishedRevision) {
        return row.draftRevision + '/' + row.publishedRevision
      }
      return row.draftRevision || row.publishedRevision || '-'
    },
    formatTime (value) {
      if (!value) {
        return '-'
      }
      const date = new Date(value)
      return isNaN(date.getTime()) ? value : date.toLocaleString()
    },
    historyEntry (record) {
      const entry = record && record.object && record.object.spec && record.object.spec.entry
      return entry ? (entry.method + ' ' + entry.path) : '-'
    },
    historyTarget (record) {
      const target = record && record.object && record.object.spec && record.object.spec.target
      return target ? (target.interface + '#' + target.method) : '-'
    }
  }
}
</script>

<style lang="less" scoped>
.route-binding-page {
  padding-bottom: 24px;
}

.intro-panel,
.panel {
  background: #fff;
  border: 1px solid #ebeef5;
  box-sizing: border-box;
}

.intro-panel {
  min-height: 54px;
  margin-top: 12px;
  padding: 10px 14px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  color: #606266;
  font-size: 12px;
}

.intro-title {
  color: #303133;
  font-weight: 600;
  margin-right: 10px;
}

.intro-actions,
.editor-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}

.binding-layout {
  margin-top: 12px;
}

.panel-header {
  min-height: 42px;
  padding: 0 12px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid #ebeef5;
  color: #303133;
  font-size: 14px;
  font-weight: 600;
}

.muted,
.record-meta {
  color: #909399;
  font-size: 12px;
  font-weight: normal;
}

.binding-list-panel {
  min-height: 520px;
}

.binding-name {
  color: #409eff;
  cursor: pointer;
}

.editor-panel {
  padding-bottom: 12px;
}

.editor-header {
  min-height: 52px;
}

.editor-header > div:first-child {
  min-width: 130px;
}

.dirty-tag {
  margin-left: 8px;
}

.record-meta {
  margin-left: 8px;
}

.editor-hint {
  margin: 10px 12px 8px;
  color: #909399;
  font-size: 12px;
  line-height: 1.5;
}

.route-editor {
  display: block;
  padding: 0 12px;
  box-sizing: border-box;
}

/deep/ .route-editor .el-textarea__inner {
  min-height: 520px !important;
  padding: 12px;
  color: #e6e6e6;
  background: #1e1e1e;
  border-color: #303133;
  border-radius: 2px;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 13px;
  line-height: 1.55;
}

.projection-panel {
  margin-top: 12px;
}

.code-block {
  margin: 0;
  padding: 12px;
  max-height: 420px;
  overflow: auto;
  box-sizing: border-box;
  white-space: pre-wrap;
  word-break: break-word;
  color: #303133;
  background: #f8f9fb;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.55;
}

.preview-meta {
  display: flex;
  gap: 20px;
  margin-bottom: 10px;
  color: #606266;
  font-size: 12px;
}

.dialog-code {
  min-height: 360px;
  max-height: 560px;
}

@media screen and (max-width: 1100px) {
  .intro-panel,
  .editor-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .intro-actions,
  .editor-actions {
    flex-wrap: wrap;
    margin-top: 8px;
  }

  .editor-header > div:first-child {
    padding-top: 10px;
  }
}
</style>
