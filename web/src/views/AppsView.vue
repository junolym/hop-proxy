<template>
  <div>
    <!-- 错误提示（非管理员查看其他用户） -->
    <div v-if="forbidden" class="bg-red-50 border border-red-200 rounded-lg p-4 mb-4">
      <p class="text-red-600 text-sm">无权查看其他用户的应用</p>
    </div>

    <!-- 查看其他用户应用的提示 -->
    <div v-else-if="viewingUserId && !forbidden" class="bg-blue-50 border border-blue-200 rounded-lg p-3 mb-4">
      <p class="text-blue-700 text-sm">
        正在查看用户 <strong>{{ viewingUsername }}</strong> 的应用
        <button @click="exitUserView" class="ml-2 text-blue-600 hover:text-blue-800 underline">返回我的应用</button>
      </p>
    </div>

    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg md:text-xl font-bold">{{ viewingUserId ? `${viewingUsername} 的应用` : '应用管理' }}</h2>
      <button v-if="!viewingUserId" @click="openCreate"
        class="px-3 py-1.5 md:px-4 md:py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
        + 添加
      </button>
    </div>

    <!-- 筛选状态提示（#59） -->
    <div v-if="activeFilterCount > 0" class="flex items-center gap-2 mb-3 text-xs text-gray-500">
      <span>已筛选 {{ sortedApps.length }}/{{ apps.length }} 个应用（{{ activeFilterCount }} 项筛选激活）</span>
      <button @click="resetAllFilters" class="text-blue-600 hover:text-blue-800">重置筛选</button>
    </div>

    <!-- 桌面端表格 -->
    <div class="hidden md:block bg-white rounded-lg border border-gray-200 overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-gray-50 text-gray-600">
          <tr>
            <th class="text-left px-4 py-3 font-medium select-none">
              <div class="flex items-center gap-1">
                <span class="cursor-pointer hover:text-blue-600" @click="toggleSort('name')">
                  名称 <span class="text-xs ml-0.5" :class="sortIndicatorClass('name')">{{ sortIndicator('name') }}</span>
                </span>
                <button :class="isFilterActive('name') ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                  class="flex-shrink-0 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                  @click.stop="openFilter($event, 'name')" title="筛选">▼</button>
              </div>
            </th>
            <th class="text-left px-4 py-3 font-medium select-none">
              <div class="flex items-center gap-1">
                <span class="cursor-pointer hover:text-blue-600" @click="toggleSort('subdomain')">
                  子域名 <span class="text-xs ml-0.5" :class="sortIndicatorClass('subdomain')">{{ sortIndicator('subdomain') }}</span>
                </span>
                <button :class="isFilterActive('subdomain') ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                  class="flex-shrink-0 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                  @click.stop="openFilter($event, 'subdomain')" title="筛选">▼</button>
              </div>
            </th>
            <th class="text-left px-4 py-3 font-medium select-none">
              <div class="flex items-center gap-1">
                <span class="cursor-pointer hover:text-blue-600" @click="toggleSort('target_url')">
                  目标地址 <span class="text-xs ml-0.5" :class="sortIndicatorClass('target_url')">{{ sortIndicator('target_url') }}</span>
                </span>
                <button :class="isFilterActive('target_url') ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                  class="flex-shrink-0 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                  @click.stop="openFilter($event, 'target_url')" title="筛选">▼</button>
              </div>
            </th>
            <th class="text-left px-4 py-3 font-medium select-none">
              <div class="flex items-center gap-1">
                <span class="cursor-pointer hover:text-blue-600" @click="toggleSort('client')">
                  客户端 <span class="text-xs ml-0.5" :class="sortIndicatorClass('client')">{{ sortIndicator('client') }}</span>
                </span>
                <button :class="isFilterActive('client') ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                  class="flex-shrink-0 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                  @click.stop="openFilter($event, 'client')" title="筛选">▼</button>
              </div>
            </th>
            <th class="text-left px-4 py-3 font-medium select-none">
              <div class="flex items-center gap-1">
                <span class="cursor-pointer hover:text-blue-600" @click="toggleSort('auth_method')">
                  认证 <span class="text-xs ml-0.5" :class="sortIndicatorClass('auth_method')">{{ sortIndicator('auth_method') }}</span>
                </span>
                <button :class="isFilterActive('auth_method') ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                  class="flex-shrink-0 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                  @click.stop="openFilter($event, 'auth_method')" title="筛选">▼</button>
              </div>
            </th>
            <th class="text-left px-4 py-3 font-medium select-none">
              <div class="flex items-center gap-1">
                <span class="cursor-pointer hover:text-blue-600" @click="toggleSort('status')">
                  状态 <span class="text-xs ml-0.5" :class="sortIndicatorClass('status')">{{ sortIndicator('status') }}</span>
                </span>
                <button :class="isFilterActive('status') ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                  class="flex-shrink-0 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                  @click.stop="openFilter($event, 'status')" title="筛选">▼</button>
              </div>
            </th>
            <th @click="toggleSort('last_used')" class="text-left px-4 py-3 font-medium cursor-pointer hover:bg-gray-100 select-none">
              最近使用 <span class="text-xs ml-0.5" :class="sortIndicatorClass('last_used')">{{ sortIndicator('last_used') }}</span>
            </th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <tr v-for="app in sortedApps" :key="app.id" class="hover:bg-gray-50">
            <td class="px-4 py-3" :class="!app.enabled ? 'text-gray-400 line-through' : ''">{{ app.name }}</td>
            <td class="px-4 py-3">
              <template v-if="proxyDomain && app.client_online">
                <a :href="appURL(app)" target="_blank"
                  class="inline-flex items-center gap-1 text-xs bg-gray-100 px-2 py-1 rounded hover:bg-blue-50 hover:text-blue-600 transition-colors">
                  {{ app.subdomain }}
                  <svg class="w-3 h-3 opacity-60" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14" /></svg>
                </a>
              </template>
              <template v-else>
                <code class="text-xs bg-gray-100 px-2 py-1 rounded">{{ app.subdomain }}</code>
              </template>
            </td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ app.target_url }}</td>
            <td class="px-4 py-3">
              <div class="flex flex-wrap gap-1">
                <span v-for="info in app.client_infos" :key="info.client_id"
                  class="inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded"
                  :class="info.client_id === '__host__' ? 'bg-blue-50 text-blue-700' : info.online ? 'bg-green-50 text-green-700' : 'bg-gray-100 text-gray-500'">
                  <span class="w-1.5 h-1.5 rounded-full"
                    :class="info.client_id === '__host__' ? 'bg-blue-500' : info.online ? 'bg-green-500' : 'bg-gray-400'"></span>
                  {{ info.client_id === '__host__' ? 'Host' : info.client_name }}
                </span>
                <span v-if="app.load_balance && app.client_infos?.length > 1"
                  class="text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">均衡</span>
              </div>
            </td>
            <td class="px-4 py-3">
              <span v-if="app.auth_method === 'token'" class="text-xs text-purple-600 bg-purple-50 px-1.5 py-0.5 rounded">票据</span>
              <span v-else-if="app.auth_method === 'sso_token'" class="text-xs text-teal-600 bg-teal-50 px-1.5 py-0.5 rounded">SSO+票据</span>
              <span v-else-if="app.auth_method === 'sso' || app.require_auth" class="text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">SSO</span>
              <span v-else class="text-xs text-gray-400">无</span>
            </td>
            <td class="px-4 py-3">
              <span class="text-xs" :class="appAvailability(app).colorClass" :title="appAvailability(app).title">{{ appAvailability(app).text }}</span>
            </td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ timeAgo(app.last_used_at) }}</td>
            <td class="px-4 py-3 text-right space-x-2">
              <button v-if="canShare(app)" @click="openShare(app)" class="text-green-600 hover:text-green-800 text-xs">分享</button>
              <button @click="toggleEnabled(app)" class="text-xs"
                :class="app.enabled ? 'text-yellow-600 hover:text-yellow-800' : 'text-green-600 hover:text-green-800'">
                {{ app.enabled ? '禁用' : '启用' }}
              </button>
              <button @click="handleClone(app)" class="text-blue-600 hover:text-blue-800 text-xs">克隆</button>
              <button @click="openEdit(app)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
              <button @click="handleDelete(app.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
            </td>
          </tr>
          <tr v-if="loading">
            <td colspan="8"><LoadingSpinner /></td>
          </tr>
          <tr v-else-if="sortedApps.length === 0">
            <td colspan="8" class="px-4 py-8 text-center text-gray-400">{{ apps.length === 0 ? '暂无应用，点击右上角添加' : '没有符合筛选条件的应用' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 移动端卡片列表 -->
    <div class="md:hidden space-y-3">
      <div v-if="loading" class="bg-white rounded-lg border border-gray-200">
        <LoadingSpinner />
      </div>
      <div v-else-if="apps.length === 0" class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        暂无应用，点击右上角添加
      </div>
      <div v-else-if="sortedApps.length === 0" class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        没有符合筛选条件的应用
      </div>
      <div v-for="app in sortedApps" :key="app.id" class="bg-white rounded-lg border border-gray-200 p-4">
        <div class="flex items-start justify-between mb-2">
          <div class="flex-1 min-w-0 mr-2">
            <div class="flex items-center gap-2 flex-wrap mb-1">
              <span class="font-medium text-sm" :class="app.enabled ? 'text-gray-900' : 'text-gray-400 line-through'">{{ app.name }}</span>
              <span v-if="app.auth_method === 'token'" class="text-xs text-purple-600 bg-purple-50 px-1.5 py-0.5 rounded">票据</span>
              <span v-else-if="app.auth_method === 'sso_token'" class="text-xs text-teal-600 bg-teal-50 px-1.5 py-0.5 rounded">SSO+票据</span>
              <span v-else-if="app.auth_method === 'sso' || app.require_auth" class="text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">SSO</span>
              <span v-if="app.load_balance && app.client_infos?.length > 1"
                class="text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">负载均衡</span>
            </div>
            <template v-if="proxyDomain && app.client_online">
              <a :href="appURL(app)" target="_blank" class="inline-flex items-center gap-1 text-xs text-blue-600 mb-1">
                {{ app.subdomain }}.{{ proxyDomain }}
                <svg class="w-3 h-3" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14" /></svg>
              </a>
            </template>
            <template v-else>
              <code class="text-xs text-gray-400 block mb-1">{{ app.subdomain }}</code>
            </template>
            <p class="text-xs text-gray-400 truncate">{{ app.target_url }}</p>
          </div>
          <span class="text-xs flex-shrink-0" :class="appAvailability(app).colorClass" :title="appAvailability(app).title">
            {{ appAvailability(app).text }}
          </span>
        </div>
        <div class="flex flex-wrap gap-1 mb-2">
          <span v-for="info in app.client_infos" :key="info.client_id"
            class="inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded"
            :class="info.client_id === '__host__' ? 'bg-blue-50 text-blue-700' : info.online ? 'bg-green-50 text-green-700' : 'bg-gray-100 text-gray-500'">
            <span class="w-1.5 h-1.5 rounded-full"
              :class="info.client_id === '__host__' ? 'bg-blue-500' : info.online ? 'bg-green-500' : 'bg-gray-400'"></span>
            {{ info.client_id === '__host__' ? 'Host' : info.client_name }}
          </span>
        </div>
        <div class="flex items-center justify-between pt-2 border-t border-gray-100">
          <span class="text-xs text-gray-400">最近使用: {{ timeAgo(app.last_used_at) }}</span>
          <div class="flex gap-3">
          <button v-if="canShare(app)" @click="openShare(app)" class="text-green-600 text-xs py-1">分享</button>
          <button @click="toggleEnabled(app)" class="text-xs py-1"
            :class="app.enabled ? 'text-yellow-600' : 'text-green-600'">
            {{ app.enabled ? '禁用' : '启用' }}
          </button>
          <button @click="handleClone(app)" class="text-blue-600 text-xs py-1">克隆</button>
          <button @click="openEdit(app)" class="text-blue-600 text-xs py-1">编辑</button>
          <button @click="handleDelete(app.id)" class="text-red-600 text-xs py-1">删除</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 创建/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showForm = false">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="p-5">
          <h3 class="text-lg font-bold mb-4">{{ formMode === 'create' ? '添加应用' : '编辑应用' }}</h3>
          <form @submit.prevent="handleSubmit" class="space-y-4">
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">名称</label>
              <input v-model="form.name" type="text" required
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">子域名</label>
              <div class="flex">
                <!-- 普通用户固定前缀 -->
                <span v-if="!isAdmin"
                  class="inline-flex items-center px-3 py-2 border border-r-0 border-gray-300 rounded-l-md bg-gray-50 text-sm text-gray-500">
                  {{ subdomainPrefix }}
                </span>
                <input v-model="form.subdomain" type="text" required pattern="[a-z0-9][a-z0-9-]*[a-z0-9]|[a-z0-9]"
                  @input="subdomainManuallyEdited = true"
                  class="flex-1 px-3 py-2 border border-gray-300 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                  :class="isAdmin ? 'rounded-md' : 'rounded-r-md'"
                  :placeholder="isAdmin ? 'my-app' : 'app-name'" />
              </div>
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">目标地址</label>
              <div class="flex">
                <!-- focus-within:z-10：按钮聚焦时整个组件抬升，ring 右侧不被地址输入框覆盖 -->
                <BaseSelect v-model="targetSchema" class="w-20 focus-within:z-10" :options="schemaOptions"
                  btn-class="w-full px-2 py-2 border border-r-0 border-gray-300 rounded-l-md !bg-gray-50 text-sm text-gray-700 focus:outline-none focus:ring-2 focus:ring-blue-500" />
                <input v-model="targetHost" type="text" required
                  class="flex-1 px-3 py-2 border border-r-0 border-gray-300 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 relative focus:z-10"
                  placeholder="域名或IP地址" />
                <input v-model="targetPort" type="text" @input="sanitizeTargetPort"
                  class="w-24 px-3 py-2 border border-gray-300 rounded-r-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 relative focus:z-10"
                  placeholder="端口" />
              </div>
              <p class="text-xs text-gray-400 mt-1">拼接结果：{{ targetUrlPreview }}</p>
              <p v-if="form.agent_key_uuid" class="text-xs text-blue-600 mt-1">
                已选代理密钥，目标地址应填 agent 的 listen 地址（scheme 为后端协议）
              </p>
              <p class="text-xs text-gray-400 mt-1">支持变量：内置变量（${host}、${subdomain} 等）与模糊子域名捕获组 $1/${1}，详见下方自定义 HTTP Header 说明；拼接结果为 协议://地址:端口（端口可空，空则使用协议默认端口）</p>
            </div>

            <!-- 多客户端选择 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-2">
                关联客户端
                <span class="text-xs text-gray-400 font-normal ml-1">（可多选，拖动调整主备优先级）</span>
              </label>
              <div class="space-y-2 border border-gray-200 rounded-md p-3 bg-gray-50">
                <!-- 本机客户端（仅管理员可见） -->
                <label v-if="isAdmin"
                  class="flex items-center gap-2 cursor-pointer hover:bg-white rounded px-2 py-1 transition-colors">
                  <input type="checkbox"
                    value="__host__"
                    :checked="form.client_ids.includes('__host__')"
                    @change="toggleClient('__host__')"
                    class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
                  <span class="flex items-center gap-1.5 text-sm text-gray-800">
                    <span class="w-2 h-2 rounded-full flex-shrink-0 bg-blue-500"></span>
                    Host
                  </span>
                  <span v-if="form.client_ids.includes('__host__')"
                    class="ml-auto text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">
                    #{{ form.client_ids.indexOf('__host__') + 1 }}
                  </span>
                </label>
                <!-- 远程客户端 -->
                <label v-for="c in clientList" :key="c.id"
                  class="flex items-center gap-2 cursor-pointer hover:bg-white rounded px-2 py-1 transition-colors">
                  <input type="checkbox"
                    :value="c.id"
                    :checked="form.client_ids.includes(c.id)"
                    @change="toggleClient(c.id)"
                    class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
                  <span class="flex items-center gap-1.5 text-sm text-gray-800">
                    <span class="w-2 h-2 rounded-full flex-shrink-0"
                      :class="c.online ? 'bg-green-500' : 'bg-gray-300'"></span>
                    {{ c.name }}
                  </span>
                  <!-- 显示优先级顺序 -->
                  <span v-if="form.client_ids.includes(c.id)"
                    class="ml-auto text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">
                    #{{ form.client_ids.indexOf(c.id) + 1 }}
                  </span>
                </label>
                <div v-if="clientList.length === 0" class="text-xs text-gray-400 text-center py-2">
                  {{ isAdmin ? '还没有远程客户端，可以只选择本机' : '暂无客户端，请先在客户端页面添加' }}
                </div>
              </div>
              <!-- 已选客户端可排序 -->
              <div v-if="form.client_ids.length > 0" class="mt-2">
                <p class="text-xs text-gray-500 mb-1.5">拖动排序（靠前为主，靠后为备），可设置独立目标地址和代理：</p>
                <div class="space-y-2">
                  <div v-for="(cid, idx) in form.client_ids" :key="cid"
                    class="bg-white border border-gray-200 rounded px-3 py-2 text-sm">
                    <div class="flex items-center gap-2">
                      <span class="text-gray-400 text-xs w-4 text-center">{{ idx + 1 }}</span>
                      <span class="flex-1 font-medium">{{ clientName(cid) }}</span>
                      <button type="button" @click="moveUp(idx)" :disabled="idx === 0"
                        class="text-gray-400 hover:text-gray-600 disabled:opacity-30 text-xs px-1">↑</button>
                      <button type="button" @click="moveDown(idx)" :disabled="idx === form.client_ids.length - 1"
                        class="text-gray-400 hover:text-gray-600 disabled:opacity-30 text-xs px-1">↓</button>
                    </div>
                    <div class="mt-2 ml-6 space-y-2">
                      <!-- 目标地址 -->
                      <div>
                        <input type="text" v-model="form.client_configs[cid].target_url"
                          class="w-full px-2 py-1 border border-gray-200 rounded text-xs focus:outline-none focus:ring-1 focus:ring-blue-400"
                          :placeholder="'目标地址（默认：' + targetUrlPreview + '）'" />
                      </div>
                      <!-- 代理选择 -->
                      <div class="bg-gray-50 rounded p-2 space-y-2">
                        <div class="flex items-center gap-2">
                          <label class="text-xs text-gray-600 w-16">代理</label>
                          <BaseSelect v-model="form.client_configs[cid].proxy_id"
                            class="flex-1"
                            :options="proxyOptionsFor(cid)"
                            btn-class="w-full px-2 py-1 border border-gray-200 rounded text-xs focus:outline-none focus:ring-1 focus:ring-blue-400"
                            @open="cid !== '__host__' && loadClientProxies(cid)" />
                        </div>
                        <p v-if="cid !== '__host__' && (!clientProxies[cid] || clientProxies[cid].length === 0)"
                          class="text-xs text-gray-400">
                          暂无代理，请在客户端管理页面添加
                        </p>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <!-- 高级选项（创建模式默认收起，编辑模式全部展开） -->
            <div v-if="formMode === 'create'">
              <button type="button" @click="advancedOpen = !advancedOpen"
                class="w-full flex items-center justify-center gap-1 py-2 text-sm text-gray-500 border border-gray-200 rounded-md hover:bg-gray-50 transition-colors">
                <svg class="w-4 h-4 transition-transform" :class="advancedOpen ? 'rotate-180' : ''" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" /></svg>
                {{ advancedOpen ? '收起高级选项' : '高级选项（认证、Header 等）' }}
              </button>
            </div>

            <div v-if="formMode === 'edit' || advancedOpen" class="space-y-4">
            <!-- 安全代理密钥 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">安全代理密钥</label>
              <p class="text-xs text-gray-400 mb-1">选择后通过 Noise 加密隧道连接 agent，目标地址填 agent 监听地址</p>
              <BaseSelect v-model="form.agent_key_uuid" :options="agentKeyOptions" />
            </div>

            <!-- 负载均衡（仅多客户端时显示） -->
            <div v-if="form.client_ids.length >= 2"
              class="flex items-center gap-2 p-3 bg-blue-50 rounded-md border border-blue-100">
              <input type="checkbox" id="lb" v-model="form.load_balance"
                class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
              <label for="lb" class="text-sm text-blue-800 cursor-pointer">
                <span class="font-medium">启用负载均衡</span>
                <span class="text-xs text-blue-600 ml-1">（随机分发到所有在线客户端，否则按顺序主备）</span>
              </label>
            </div>

            <!-- 访问认证 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">认证方式</label>
              <BaseSelect v-model="form.auth_method"
                :options="authMethodOptions"
                :disabled="!isAdmin" />
              <p v-if="!isAdmin" class="text-xs text-gray-400 mt-1">普通用户必须启用认证</p>
              <p v-else-if="form.auth_method === 'token'" class="text-xs text-purple-600 mt-1">需要通过 Authorization header 提供有效的访问票据才能访问</p>
              <p v-else-if="form.auth_method === 'sso_token'" class="text-xs text-teal-600 mt-1">票据优先：有效票据直接放行；无票据时回退 SSO（浏览器重定向，API 客户端返回 401）</p>
            </div>

            <!-- SSO 扩展选项 -->
            <div v-if="form.auth_method === 'sso' || form.auth_method === 'sso_token'" class="bg-blue-50 rounded-md p-3 border border-blue-100 space-y-3">
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-1">授权用户</label>
                <div class="flex gap-4">
                  <label class="flex items-center gap-2 cursor-pointer">
                    <input type="radio" v-model="form.allowed_users" value="owner"
                      class="w-4 h-4 border-gray-300 text-blue-600 focus:ring-blue-500" />
                    <span class="text-sm text-gray-700">仅限当前用户</span>
                  </label>
                  <label class="flex items-center gap-2 cursor-pointer">
                    <input type="radio" v-model="form.allowed_users" value="all"
                      class="w-4 h-4 border-gray-300 text-blue-600 focus:ring-blue-500" />
                    <span class="text-sm text-gray-700">允许所有用户</span>
                  </label>
                </div>
                <p class="text-xs text-gray-400 mt-1">选择「仅限当前用户」时，只有应用创建者可访问</p>
              </div>
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-1">Cookie 过期时间</label>
                <BaseSelect v-model.number="form.sso_cookie_max_age" :options="cookieMaxAgeSelectOptions" />
              </div>

              <!-- SSO 二次验证 -->
              <div v-if="showSecondFactor">
                <label class="block text-sm font-medium text-gray-700 mb-1">SSO 二次验证</label>
                <BaseSelect v-model="form.second_factor" :options="secondFactorOptions" />
                <p class="text-xs text-gray-400 mt-1">授权访问该应用时需完成所选方式的验证，开启后不允许快速登录</p>
              </div>
              <p v-else-if="form.auth_method === 'sso' || form.auth_method === 'sso_token'" class="text-xs text-gray-400">
                启用 TOTP 或注册通行密钥后可在此设置应用 SSO 二次验证
              </p>

              <!-- 路径豁免 -->
              <div>
                <label class="block text-sm font-medium text-gray-700 mb-1">路径豁免</label>
                <p class="text-xs text-gray-400 mb-2">以下路径无需 SSO 认证即可直接访问，支持精确匹配和前缀匹配（以 / 结尾表示前缀）</p>
                <!-- 快捷添加按钮 -->
                <div class="flex flex-wrap gap-1.5 mb-2">
                  <button type="button" v-for="preset in exemptPathPresets" :key="preset"
                    @click="addExemptPath(preset)"
                    class="text-xs px-2 py-0.5 bg-white border border-blue-200 text-blue-600 rounded hover:bg-blue-50 transition-colors">
                    {{ preset }}
                  </button>
                </div>
                <div class="space-y-1.5">
                  <div v-for="(_, idx) in form.exempt_paths" :key="idx" class="flex gap-2 items-center">
                    <input type="text" v-model="form.exempt_paths[idx]"
                      class="flex-1 px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                      placeholder="/favicon.ico 或 /static/" />
                    <button type="button" @click="removeExemptPath(idx)"
                      class="text-red-500 hover:text-red-700 text-sm px-2">×</button>
                  </div>
                </div>
                <button type="button" @click="addExemptPath('')"
                  class="text-sm text-blue-600 hover:text-blue-800 flex items-center gap-1 mt-2">
                  <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" /></svg>
                  添加路径
                </button>
              </div>
            </div>

            <!-- 请求头缺省处理 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">请求头缺省处理</label>
              <BaseSelect v-model="form.header_mode" :options="headerModeOptions" />
              <p class="text-xs text-gray-400 mt-1">三种模式下自定义 Header 均生效（含通过自定义 Header 显式配置 X-Forwarded-* / Origin）</p>
            </div>

            <!-- 自定义 HTTP Header -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">自定义 HTTP Header</label>
              <p class="text-xs text-gray-400 mb-2">转发请求时添加额外的 Header，例如目标地址是 IP 时设置 Host 字段</p>
              <p class="text-xs text-gray-400 mb-2">值支持变量：内置变量 <code class="bg-gray-100 px-1 rounded">${host}</code> <code class="bg-gray-100 px-1 rounded">${subdomain}</code> <code class="bg-gray-100 px-1 rounded">${proto}</code> <code class="bg-gray-100 px-1 rounded">${remote_ip}</code> <code class="bg-gray-100 px-1 rounded">${method}</code> <code class="bg-gray-100 px-1 rounded">${path}</code> <code class="bg-gray-100 px-1 rounded">${query}</code> <code class="bg-gray-100 px-1 rounded">${user_id}</code> <code class="bg-gray-100 px-1 rounded">${user_name}</code> <code class="bg-gray-100 px-1 rounded">${app_id}</code> <code class="bg-gray-100 px-1 rounded">${app_name}</code>，模糊子域名捕获组 <code class="bg-gray-100 px-1 rounded">$1</code> / <code class="bg-gray-100 px-1 rounded">${1}</code>（目标地址、跳转目标同样支持）</p>
              <div class="space-y-2">
                <div v-for="(_, key) in form.custom_headers" :key="key" class="flex gap-2 items-center">
                  <input type="text" :value="key" @change="updateHeaderKey(key, ($event.target as HTMLInputElement).value)"
                    class="flex-1 px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                    placeholder="Header 名称" />
                  <input type="text" v-model="form.custom_headers[key]"
                    class="flex-1 px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                    placeholder="Header 值" />
                  <button type="button" @click="removeHeader(key)"
                    class="text-red-500 hover:text-red-700 text-sm px-2">×</button>
                </div>
                <button type="button" @click="addHeader"
                  class="text-sm text-blue-600 hover:text-blue-800 flex items-center gap-1">
                  <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" /></svg>
                  添加 Header
                </button>
              </div>
            </div>

            <!-- 代理配置覆盖（#47） -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">代理配置覆盖</label>
              <p class="text-xs text-gray-400 mb-2">覆盖全局默认的限制参数，仅对本应用生效；大小支持 k/m/g，时长支持 s/m/h（0 表示不限）</p>
              <div class="space-y-2">
                <div v-for="key in Object.keys(form.proxy_config)" :key="key" class="flex gap-2 items-center">
                  <span class="flex-1 min-w-0 truncate px-2 py-1.5 bg-gray-100 border border-gray-200 rounded text-sm font-mono text-gray-700" :title="proxyKeyDesc(key)">{{ key }}</span>
                  <input type="text" v-model="form.proxy_config[key]"
                    class="flex-none w-32 px-2 py-1.5 border border-gray-300 rounded text-sm font-mono focus:outline-none focus:ring-1 focus:ring-blue-400"
                    placeholder="如 100m 或 60s" />
                  <button type="button" @click="removeProxyOverride(key)"
                    class="text-red-500 hover:text-red-700 text-sm px-2">删除</button>
                </div>
                <div v-if="proxyKeyOptions().length > 0" class="flex gap-2 items-center">
                  <div class="flex-1 min-w-0">
                    <BaseSelect v-model="proxyOverrideNew.key" :options="proxyKeyOptions()"
                      btn-class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400 font-mono" />
                  </div>
                  <input type="text" v-model="proxyOverrideNew.value"
                    class="flex-none w-32 px-2 py-1.5 border border-gray-300 rounded text-sm font-mono focus:outline-none focus:ring-1 focus:ring-blue-400"
                    placeholder="覆盖值" />
                  <button type="button" @click="addProxyOverride" :disabled="!proxyOverrideNew.key || !proxyOverrideNew.value"
                    class="text-sm text-blue-600 hover:text-blue-800 disabled:text-gray-300 disabled:cursor-not-allowed px-2">添加</button>
                </div>
              </div>
            </div>
            </div><!-- /高级选项 -->

            <!-- 路由规则 -->
            <div v-if="formMode === 'edit'">
              <label class="block text-sm font-medium text-gray-700 mb-1">路由规则</label>
              <p class="text-xs text-gray-400 mb-2">可选：按规则覆盖认证、目标地址或路径改写，未匹配走应用默认配置</p>

              <!-- 规则列表 -->
              <div class="space-y-2 mb-3">
                <div v-if="routes.length === 0" class="text-center py-4 text-gray-400 text-sm bg-gray-50 rounded-md border border-dashed border-gray-300">
                  暂无路由规则，所有请求使用应用默认配置
                </div>
                <div v-for="route in routes" :key="route.id"
                  class="bg-gray-50 border border-gray-200 rounded px-3 py-2 text-sm">
                  <div class="flex items-start justify-between gap-2">
                    <div class="flex-1 min-w-0 space-y-1">
                      <div class="flex flex-wrap gap-x-2 gap-y-0.5 text-xs">
                        <span v-if="route.client_id" class="inline-flex items-center bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono">{{ clientName(route.client_id) }}</span>
                        <span v-if="route.method" class="inline-flex items-center bg-green-50 text-green-700 px-1.5 py-0.5 rounded uppercase font-bold" :class="{'!bg-purple-50 !text-purple-700': route.method === '*'}">{{ route.method === '*' ? '任意' : route.method }}</span>
                        <code v-if="route.path_pattern" class="bg-yellow-50 text-yellow-800 px-1.5 py-0.5 rounded">{{ route.path_pattern }}</code>
                      </div>
                      <div class="flex flex-wrap gap-x-2 gap-y-0.5 text-xs mt-1">
                        <span v-if="route.auth_method" class="inline-flex items-center px-1.5 py-0.5 rounded"
                          :class="{
                            'bg-red-50 text-red-600': route.auth_method === 'none',
                            'bg-blue-50 text-blue-600': route.auth_method === 'sso',
                            'bg-teal-50 text-teal-600': route.auth_method === 'sso_token',
                            'bg-purple-50 text-purple-600': route.auth_method === 'token'
                          }">
                          认证: {{ authMethodLabel(route.auth_method) }}
                        </span>
                        <span v-if="route.target_url" class="inline-flex items-center bg-orange-50 text-orange-700 px-1.5 py-0.5 rounded truncate max-w-[12rem]" :title="route.target_url">
                          代理: {{ route.target_url }}
                        </span>
                        <span v-if="route.path_rewrite" class="inline-flex items-center bg-teal-50 text-teal-700 px-1.5 py-0.5 rounded truncate max-w-[12rem]" :title="route.path_rewrite">
                          改写: {{ route.path_rewrite }}
                        </span>
                        <span class="text-gray-400">优先级: {{ route.priority }}</span>
                      </div>
                    </div>
                    <div class="flex items-center gap-2 flex-shrink-0">
                      <button type="button" @click="openEditRoute(route)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
                      <button type="button" @click="handleDeleteRoute(route.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
                    </div>
                  </div>
                </div>
              </div>

              <!-- 添加/编辑规则弹窗 -->
              <div v-if="showRouteForm" class="border border-blue-200 bg-blue-50/30 rounded-md p-3 space-y-3 mb-2">
                <h4 class="text-sm font-medium text-gray-800">{{ editingRouteId !== null ? '编辑路由规则' : '添加路由规则' }}</h4>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <!-- 来源客户端 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">来源客户端</label>
                    <BaseSelect v-model="routeForm.client_id"
                      :options="routeClientOptions"
                      btn-class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400" />
                    <p class="text-xs text-gray-400 mt-0.5">仅匹配从该客户端本地代理进入的请求；公网（域名/隧道）请求不命中指定了来源客户端的规则</p>
                  </div>
                  <!-- HTTP 方法 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">HTTP 方法</label>
                    <BaseSelect v-model="routeForm.method"
                      :options="routeMethodOptions"
                      btn-class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400" />
                  </div>
                </div>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <!-- 路径模式 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">路径模式</label>
                    <input type="text" v-model="routeForm.path_pattern"
                      class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                      placeholder="/api/v1/* （空=任意）" />
                    <p class="text-xs text-gray-400 mt-0.5">精确或前缀匹配（以 / 结尾为前缀），留空=任意</p>
                  </div>
                  <!-- 优先级 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">优先级</label>
                    <input type="number" v-model.number="routeForm.priority"
                      class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                      placeholder="0" min="0" />
                    <p class="text-xs text-gray-400 mt-0.5">数字越小越优先匹配</p>
                  </div>
                </div>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <!-- 认证方式覆盖 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">认证方式覆盖</label>
                    <BaseSelect v-model="routeForm.auth_method"
                      :options="routeAuthMethodOptions"
                      btn-class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400" />
                  </div>
                  <!-- 目标地址覆盖 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">目标地址覆盖</label>
                    <input type="text" v-model="routeForm.target_url"
                      class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                      placeholder="http://localhost:8080（留空=不覆盖）" />
                    <p class="text-xs text-gray-400 mt-0.5">支持变量，同应用目标地址</p>
                  </div>
                </div>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <!-- 路径改写 -->
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">路径改写</label>
                    <input type="text" v-model="routeForm.path_rewrite"
                      class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                      placeholder="/（留空=不改写）" />
                    <p class="text-xs text-gray-400 mt-0.5">命中规则时按前缀替换转发路径（须以 / 开头），查询串保留，留空=不改写</p>
                  </div>
                </div>
                <div v-if="routeFormError" class="text-sm text-red-600 bg-red-50 px-2 py-1.5 rounded">{{ routeFormError }}</div>
                <div class="flex gap-2">
                  <button type="button" @click="showRouteForm = false; resetRouteForm()"
                    class="px-3 py-1.5 text-sm text-gray-600 border border-gray-300 rounded-md hover:bg-gray-50">取消</button>
                  <button type="button" @click="handleSubmitRoute"
                    class="px-4 py-1.5 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
                    {{ editingRouteId !== null ? '保存修改' : '添加规则' }}
                  </button>
                </div>
              </div>

              <button type="button" v-if="!showRouteForm" @click="openCreateRoute"
                class="text-sm text-blue-600 hover:text-blue-800 flex items-center gap-1">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" /></svg>
                添加规则
              </button>
            </div>

            <!-- 跳转路径 -->
            <div v-if="formMode === 'edit'">
              <label class="block text-sm font-medium text-gray-700 mb-1">跳转路径</label>
              <p class="text-xs text-gray-400 mb-2">命中规则时返回 301/302 重定向，不代理请求。两阶段匹配：先按优先级匹配所有精确规则，无命中再按优先级匹配正则规则。</p>

              <div class="space-y-2 mb-3">
                <div v-if="redirects.length === 0" class="text-center py-4 text-gray-400 text-sm bg-gray-50 rounded-md border border-dashed border-gray-300">
                  暂无跳转规则，所有请求正常代理
                </div>
                <div v-for="rd in redirects" :key="rd.id"
                  class="bg-gray-50 border border-gray-200 rounded px-3 py-2 text-sm">
                  <div class="flex items-start justify-between gap-2">
                    <div class="flex-1 min-w-0 space-y-1">
                      <div class="flex flex-wrap gap-x-2 gap-y-0.5 text-xs">
                        <span class="inline-flex items-center px-1.5 py-0.5 rounded font-medium"
                          :class="rd.match_type === 'regex' ? 'bg-purple-50 text-purple-700' : 'bg-gray-200 text-gray-700'">
                          {{ rd.match_type === 'regex' ? '正则' : '精确' }}
                        </span>
                        <code class="bg-yellow-50 text-yellow-800 px-1.5 py-0.5 rounded">{{ rd.match_path }}</code>
                        <span class="text-gray-400">→</span>
                        <code class="bg-orange-50 text-orange-700 px-1.5 py-0.5 rounded truncate max-w-[14rem]" :title="rd.redirect_target">{{ rd.redirect_target }}</code>
                      </div>
                      <div class="flex flex-wrap gap-x-2 gap-y-0.5 text-xs mt-1">
                        <span class="inline-flex items-center px-1.5 py-0.5 rounded"
                          :class="rd.status_code === 301 ? 'bg-red-50 text-red-600' : 'bg-blue-50 text-blue-600'">
                          {{ rd.status_code === 301 ? '301 永久' : '302 临时' }}
                        </span>
                        <span class="text-gray-400">优先级: {{ rd.priority }}</span>
                        <span v-if="rd.match_include_query" class="inline-flex items-center px-1.5 py-0.5 rounded bg-teal-50 text-teal-700">含 query</span>
                      </div>
                    </div>
                    <div class="flex items-center gap-2 flex-shrink-0">
                      <button type="button" @click="openEditRedirect(rd)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
                      <button type="button" @click="handleDeleteRedirect(rd.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
                    </div>
                  </div>
                </div>
              </div>

              <div v-if="showRedirectForm" class="border border-blue-200 bg-blue-50/30 rounded-md p-3 space-y-3 mb-2">
                <h4 class="text-sm font-medium text-gray-800">{{ editingRedirectId !== null ? '编辑跳转规则' : '添加跳转规则' }}</h4>
                <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">匹配类型</label>
                    <BaseSelect v-model="redirectForm.match_type"
                      :options="redirectMatchTypeOptions"
                      btn-class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400" />
                  </div>
                  <div>
                    <label class="block text-xs font-medium text-gray-600 mb-1">优先级</label>
                    <input type="number" v-model.number="redirectForm.priority"
                      class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                      placeholder="0" min="0" />
                    <p class="text-xs text-gray-400 mt-0.5">数字越小越优先匹配（同类规则内）</p>
                  </div>
                </div>
                <div>
                  <label class="block text-xs font-medium text-gray-600 mb-1">匹配路径</label>
                  <input type="text" v-model="redirectForm.match_path"
                    class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                    :placeholder="redirectForm.match_type === 'regex' ? '^/api/(.*)$' : '/old-path'" />
                  <p class="text-xs text-gray-400 mt-0.5">
                    <span v-if="redirectForm.match_type === 'exact'">精确路径，如 /old</span>
                    <span v-else>Go 正则（RE2），如 ^/api/(.*)$，用括号捕获组供跳转目标替换</span>
                  </p>
                </div>
                <div class="flex items-center gap-2 sm:col-span-2">
                  <input type="checkbox" id="redirect-match-include-query" v-model="redirectForm.match_include_query"
                    class="h-4 w-4 text-blue-600 border-gray-300 rounded focus:ring-blue-400" />
                  <label for="redirect-match-include-query" class="text-xs text-gray-700 select-none">
                    匹配含 query 的完整路径
                    <span class="text-gray-400">（开启后用 path?query 比对，可避免 / → /?token=xxx 死循环；默认仅比对 path）</span>
                  </label>
                </div>
                <div>
                  <label class="block text-xs font-medium text-gray-600 mb-1">跳转目标</label>
                  <input type="text" v-model="redirectForm.redirect_target"
                    class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
                    :placeholder="redirectForm.match_type === 'regex' ? '/v2/api/$1 或 https://example.com/$1' : '/new 或 https://example.com/new'" />
                  <p class="text-xs text-gray-400 mt-0.5">
                    <span v-if="redirectForm.match_type === 'regex'">支持 $1 ${1} 占位符替换捕获组，</span>
                    支持内置变量（如 ${host}、${path}、${query}），
                    / 开头=站内跳转（不改 scheme/host），http(s):// 开头=站外跳转
                  </p>
                </div>
                <div>
                  <label class="block text-xs font-medium text-gray-600 mb-1">跳转类型</label>
                  <BaseSelect v-model.number="redirectForm.status_code"
                    :options="redirectStatusCodeOptions"
                    btn-class="w-full px-2 py-1.5 border border-gray-300 rounded text-sm focus:outline-none focus:ring-1 focus:ring-blue-400" />
                </div>
                <div v-if="redirectFormError" class="text-sm text-red-600 bg-red-50 px-2 py-1.5 rounded">{{ redirectFormError }}</div>
                <div class="flex gap-2">
                  <button type="button" @click="showRedirectForm = false; resetRedirectForm()"
                    class="px-3 py-1.5 text-sm text-gray-600 border border-gray-300 rounded-md hover:bg-gray-50">取消</button>
                  <button type="button" @click="handleSubmitRedirect"
                    class="px-4 py-1.5 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
                    {{ editingRedirectId !== null ? '保存修改' : '添加规则' }}
                  </button>
                </div>
              </div>

              <button type="button" v-if="!showRedirectForm" @click="openCreateRedirect"
                class="text-sm text-blue-600 hover:text-blue-800 flex items-center gap-1">
                <svg class="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" /></svg>
                添加规则
              </button>
            </div>

            <div v-if="formError" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ formError }}</div>

            <div class="flex gap-2">
              <button type="button" @click="showForm = false"
                class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
              <button type="submit"
                class="flex-1 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
                {{ formMode === 'create' ? '创建' : '保存' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
    <!-- 分享码创建弹窗 -->
    <div v-if="showShareForm" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showShareForm = false">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="p-5">
          <h3 class="text-lg font-bold mb-1">分享应用</h3>
          <p class="text-sm text-gray-500 mb-4">{{ shareApp?.name }} ({{ shareApp?.subdomain }})</p>
          <form @submit.prevent="handleShareSubmit" class="space-y-4">
            <!-- 模糊匹配应用的具体子域名 -->
            <div v-if="shareApp && isFuzzySubdomain(shareApp.subdomain)">
              <label class="block text-sm font-medium text-gray-700 mb-1">具体子域名</label>
              <p class="text-xs text-gray-400 mb-1">应用模式为 <code class="bg-gray-100 px-1 rounded">{{ shareApp.subdomain }}</code>，请填写命中的完整子域名（仅小写字母、数字、连字符）</p>
              <input v-model="shareForm.concrete_subdomain" type="text" required
                placeholder="如 abc-dev"
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">分享码有效期</label>
              <BaseSelect v-model.number="shareForm.expires_in_secs" :options="shareExpirySelectOptions" />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">Cookie 有效期</label>
              <p class="text-xs text-gray-400 mb-1">用户访问后获得的 SSO Cookie 有效期</p>
              <BaseSelect v-model.number="shareForm.cookie_ttl" :options="shareCookieTTLSelectOptions" />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">跳转次数限制</label>
              <p class="text-xs text-gray-400 mb-1">每个新用户访问消耗一次（已有有效 cookie 时不消耗）</p>
              <input v-model.number="shareForm.max_uses" type="number" required min="1" max="1000"
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">跳转路径</label>
              <p class="text-xs text-gray-400 mb-1">用户兑换后跳转到的应用内路径，留空表示 /，必须以 / 开头</p>
              <input v-model="shareForm.redirect_path" type="text" placeholder="/"
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>
            <div v-if="shareFormError" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ shareFormError }}</div>
            <div class="flex gap-2">
              <button type="button" @click="showShareForm = false"
                class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
              <button type="submit"
                class="flex-1 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">创建</button>
            </div>
          </form>
        </div>
      </div>
    </div>

    <!-- 分享链接展示弹窗 -->
    <div v-if="newShareURL" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-2">分享码已创建</h3>
        <p class="text-sm text-gray-500 mb-4">将以下链接分享给他人，访问后将自动获得该应用的 SSO Cookie。</p>
        <div class="bg-gray-50 border border-gray-200 rounded p-3 mb-4">
          <p class="text-xs text-gray-500 mb-1">分享链接</p>
          <code class="text-sm break-all select-all text-gray-800">{{ newShareURL }}</code>
        </div>
        <button @click="copyNewShareURL"
          class="w-full mb-2 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
          {{ shareCopied ? '✓ 已复制' : '复制链接' }}
        </button>
        <button @click="newShareURL = ''; shareCopied = false"
          class="w-full py-2 text-sm text-gray-600 border border-gray-300 rounded-md">关闭</button>
      </div>
    </div>

    <!-- 表头筛选弹层（#59，模式同系统日志页） -->
    <div v-if="filterPopover.open" class="fixed inset-0 z-40" @click="closeFilter">
      <div class="absolute bg-white border border-gray-200 rounded shadow-lg min-w-[200px] max-h-[320px] flex flex-col"
        :style="popoverStyle"
        @click.stop>
        <!-- 文本列：关键词包含匹配 -->
        <div v-if="filterMode === 'text'" class="p-2 border-b border-gray-100">
          <input v-model="filterText" type="text" placeholder="输入关键词，包含匹配..."
            @keydown.enter="applyFilter"
            class="w-full px-2 py-1 text-xs border border-gray-300 rounded focus:outline-none focus:ring-1 focus:ring-blue-500" />
        </div>
        <!-- 枚举列：搜索 + 复选列表 -->
        <template v-else>
          <div class="p-2 border-b border-gray-100">
            <input v-model="filterSearch" type="text" placeholder="搜索..."
              class="w-full px-2 py-1 text-xs border border-gray-300 rounded focus:outline-none focus:ring-1 focus:ring-blue-500" />
          </div>
          <div class="flex-1 overflow-y-auto py-1">
            <div v-if="filteredFilterOptions.length === 0" class="px-3 py-2 text-xs text-gray-400">无可选项</div>
            <label v-for="o in filteredFilterOptions" :key="o.value"
              class="flex items-center px-3 py-1 text-xs hover:bg-gray-50 cursor-pointer">
              <input type="checkbox" :checked="localSelection.includes(o.value)" @change="toggleSelect(o.value)"
                class="mr-2" />
              <span>{{ o.label }}</span>
            </label>
          </div>
        </template>
        <div class="flex items-center justify-between p-2 border-t border-gray-100">
          <button @click="clearFilter" class="px-2 py-1 text-xs text-gray-500 hover:text-red-600">清空</button>
          <div class="flex gap-2">
            <button @click="closeFilter"
              class="px-2 py-1 text-xs bg-gray-100 text-gray-700 rounded hover:bg-gray-200">取消</button>
            <button @click="applyFilter"
              class="px-2 py-1 text-xs bg-blue-600 text-white rounded hover:bg-blue-700">应用</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { listApps, createApp, updateApp, deleteApp, duplicateApp, type App, listAppRoutes, createAppRoute, updateAppRoute, deleteAppRoute, type AppRoute, listAppRedirects, createAppRedirect, updateAppRedirect, deleteAppRedirect, type AppRedirect } from '../api/apps'
import { listClients, type Client } from '../api/clients'
import { getSettings, getUserSettings, getProxyConfigKeys, parseProxyConfigText, formatProxyConfigText, type ProxyConfigKey } from '../api/settings'
import { useAuthStore } from '../stores/auth'
import { listUsers, type User } from '../api/admin'
import { listProxies, type ClientProxy } from '../api/proxies'
import { createShareCode } from '../api/share_codes'
import { listAgentKeys, type AgentKey } from '../api/agent_keys'
import { listCredentials } from '../api/webauthn'
import BaseSelect from '../components/BaseSelect.vue'
import LoadingSpinner from '../components/LoadingSpinner.vue'

// 格式化相对时间
function timeAgo(dateStr: string | null): string {
  if (!dateStr) return '从未'
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffSec = Math.floor(diffMs / 1000)
  const diffMin = Math.floor(diffSec / 60)
  const diffHour = Math.floor(diffMin / 60)
  const diffDay = Math.floor(diffHour / 24)
  const diffWeek = Math.floor(diffDay / 7)
  const diffMonth = Math.floor(diffDay / 30)
  const diffYear = Math.floor(diffDay / 365)

  if (diffSec < 60) return '刚刚'
  if (diffMin < 60) return `${diffMin} 分钟前`
  if (diffHour < 24) return `${diffHour} 小时前`
  if (diffDay < 7) return `${diffDay} 天前`
  if (diffWeek < 4) return `${diffWeek} 周前`
  if (diffMonth < 12) return `${diffMonth} 个月前`
  return `${diffYear} 年前`
}

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const isAdmin = computed(() => authStore.user?.is_admin ?? false)
const subdomainPrefix = computed(() => (authStore.user?.username?.toLowerCase() ?? '') + '-')
const apps = ref<App[]>([])
const loading = ref(true)
const clientList = ref<Client[]>([])
const proxyDomain = ref('')
const showForm = ref(false)
const formMode = ref<'create' | 'edit'>('create')
const formError = ref('')
const editId = ref(0)

// 高级选项折叠区（创建模式默认收起；编辑模式始终展开）
const advancedOpen = ref(false)
// 子域名是否已被人工修改（修改后停止从名称自动推导）
const subdomainManuallyEdited = ref(false)

// 从名称推导子域名：小写、空白/下划线转连字符、去除非法字符（如中文）
function deriveSubdomain(name: string): string {
  return name.toLowerCase()
    .replace(/[\s_]+/g, '-')
    .replace(/[^a-z0-9-]/g, '')
    .replace(/-{2,}/g, '-')
    .replace(/^-+|-+$/g, '')
}

// 查看其他用户应用相关
const viewingUserId = ref<number | null>(null)
const viewingUsername = ref('')
const forbidden = ref(false)
const users = ref<User[]>([])

// 客户端配置类型
interface ClientConfig {
  target_url: string
  proxy_id: number | null
}

// 创建空的客户端配置
function emptyClientConfig(): ClientConfig {
  return {
    target_url: '',
    proxy_id: null,
  }
}

// 客户端代理列表（按客户端 ID 缓存）
const clientProxies = ref<Record<string, ClientProxy[]>>({})
const agentKeys = ref<AgentKey[]>([])

// BaseSelect 用的代理选项（按客户端 ID）
function proxyOptionsFor(cid: string) {
  return [
    { value: null as string | number | null, label: '无' },
    ...(clientProxies.value[cid] || []).map(p => ({ value: p.id, label: `${p.name} (${p.proxy_address})` })),
  ]
}

const form = ref({
  name: '',
  subdomain: '',
  client_ids: [] as string[],
  client_configs: {} as Record<string, ClientConfig>,
  auth_method: 'sso' as 'none' | 'sso' | 'sso_token' | 'token',
  allowed_users: 'owner' as 'owner' | 'all',
  sso_cookie_max_age: 86400,
  load_balance: false,
  custom_headers: {} as Record<string, string>,
  header_mode: 'auto_xff' as 'auto_xff' | 'auto_origin' | 'none',
  proxy_config: {} as Record<string, string>,
  exempt_paths: [] as string[],
  agent_key_uuid: '' as string,
  second_factor: '' as '' | 'totp' | 'passkey',
})

// ===== 目标地址三段输入（schema/地址/端口，前端拼接，后端不感知）=====
const targetSchema = ref<'http' | 'https'>('http')
const targetHost = ref('')
const targetPort = ref('')

// 协议下拉选项（BaseSelect）
const schemaOptions = [
  { value: 'http', label: 'http' },
  { value: 'https', label: 'https' },
]

// 解析现有 target_url 为三段回填表单；变量与 IPv6 方括号原样保留
// 端口段可空（空 = 不指定，连接时按协议默认端口 http=80/https=443），也可为纯数字或变量形式（$n、${n}、${name} 及 8${1} 等混合，后端 pkg/vars 运行时展开）
function parseTargetUrl(raw: string): { schema: 'http' | 'https'; host: string; port: string } {
  if (!raw) return { schema: 'http', host: '', port: '' }

  let rest = raw
  let schema: 'http' | 'https' = 'http'
  if (rest.toLowerCase().startsWith('https://')) {
    schema = 'https'
    rest = rest.slice(8)
  } else if (rest.toLowerCase().startsWith('http://')) {
    rest = rest.slice(7)
  }

  // 遗留的带路径 target_url（如 host:8080/path）：整段保留，端口留空，避免再次保存时损坏
  if (rest.includes('/')) {
    return { schema, host: rest, port: '' }
  }

  // 端口段支持变量形式：以数字或 $ 开头，后跟字母/数字/下划线/$/{/}（宽进严出，未知变量后端原样输出）
  const m = rest.match(/^(.*):([0-9$][0-9a-zA-Z_$}{]*)$/)
  if (m && m[1] && m[2]) {
    return { schema, host: m[1], port: m[2] }
  }
  // 无端口：留空，不回填协议默认端口（保存时保持 协议://地址 原样）
  return { schema, host: rest, port: '' }
}

// 拼接三段为完整 target_url；端口为空或 host 含路径时不拼端口
function assembleTargetUrl(): string {
  const host = targetHost.value.trim()
  const port = targetPort.value.trim()
  if (!port || host.includes('/')) {
    return `${targetSchema.value}://${host}`
  }
  return `${targetSchema.value}://${host}:${port}`
}

const targetUrlPreview = computed(() => assembleTargetUrl())

// 端口允许纯数字或变量形式（$n/${n}/${name}），仅剔除合法集合之外的字符；不做变量名精确校验（宽进严出）
function sanitizeTargetPort() {
  targetPort.value = targetPort.value.replace(/[^0-9a-zA-Z_$}{]/g, '')
}

// ===== 代理配置覆盖（#47）=====
// 可覆盖 key 元数据（从服务端 /api/proxy-config/keys 拉取，仅应用级 key）
const appProxyKeys = ref<ProxyConfigKey[]>([])
// 待添加的覆盖项（下拉选择 key + 值输入）
const proxyOverrideNew = ref({ key: '', value: '' })

function proxyKeyOptions() {
  return appProxyKeys.value
    .filter(k => !form.value.proxy_config[k.key]) // 已添加的 key 不再出现
    .map(k => ({ value: k.key, label: k.key, description: `${k.desc}，默认 ${k.default}` }))
}

function addProxyOverride() {
  const key = proxyOverrideNew.value.key.trim()
  const value = proxyOverrideNew.value.value.trim()
  if (!key || !value) return
  form.value.proxy_config[key] = value
  proxyOverrideNew.value = { key: '', value: '' }
}

function removeProxyOverride(key: string) {
  delete form.value.proxy_config[key]
}

async function loadAppProxyKeys() {
  const res = await getProxyConfigKeys()
  if (res.data) {
    appProxyKeys.value = res.data.filter(k => k.app_override)
  }
}

function proxyKeyDesc(key: string): string {
  return appProxyKeys.value.find(k => k.key === key)?.desc || ''
}

// ===== 路由规则相关 =====
const routes = ref<AppRoute[]>([])
const showRouteForm = ref(false)
const editingRouteId = ref<number | null>(null)
const routeFormError = ref('')
const routeForm = ref({
  client_id: '',
  method: '',
  path_pattern: '',
  auth_method: '',
  target_url: '',
  path_rewrite: '',
  priority: 0,
})

function authMethodLabel(method: string): string {
  const map: Record<string, string> = { none: '无', sso: '仅SSO', sso_token: 'SSO+票据', token: '票据' }
  return map[method] || method
}

function resetRouteForm() {
  routeForm.value = { client_id: '', method: '', path_pattern: '', auth_method: '', target_url: '', path_rewrite: '', priority: 0 }
  editingRouteId.value = null
  routeFormError.value = ''
}

function openCreateRoute() {
  resetRouteForm()
  showRouteForm.value = true
}

function openEditRoute(route: AppRoute) {
  editingRouteId.value = route.id
  routeForm.value = {
    client_id: route.client_id || '',
    method: route.method || '',
    path_pattern: route.path_pattern || '',
    auth_method: route.auth_method || '',
    target_url: route.target_url || '',
    path_rewrite: route.path_rewrite || '',
    priority: route.priority,
  }
  routeFormError.value = ''
  showRouteForm.value = true
}

async function loadRoutes(appId: number) {
  const res = await listAppRoutes(appId)
  if (res.data) routes.value = res.data
}

async function handleSubmitRoute() {
  const f = routeForm.value
  // 校验：三者不能全为空
  const allEmpty = (!f.client_id || f.client_id === '*') && (!f.method || f.method === '*') && !f.path_pattern
  if (allEmpty) {
    routeFormError.value = '来源客户端、请求方法、路径模式三者不能同时为「任意」'
    return
  }
  // 校验：至少要覆盖一个属性
  if (!f.auth_method && !f.target_url && !f.path_rewrite) {
    routeFormError.value = '必须指定认证方式覆盖、目标地址覆盖或路径改写（至少一个）'
    return
  }
  // 校验：路径改写非空时必须以 / 开头
  if (f.path_rewrite && !f.path_rewrite.startsWith('/')) {
    routeFormError.value = '路径改写必须以 / 开头'
    return
  }

  const data = { ...f }
  routeFormError.value = ''

  if (editingRouteId.value !== null) {
    const res = await updateAppRoute(editingRouteId.value, data)
    if (res.error) { routeFormError.value = res.error; return }
  } else {
    const appId = editId.value
    const res = await createAppRoute(appId, data)
    if (res.error) { routeFormError.value = res.error; return }
  }
  showRouteForm.value = false
  await loadRoutes(editId.value)
}

async function handleDeleteRoute(routeId: number) {
  if (!confirm('确定删除此路由规则？')) return
  await deleteAppRoute(routeId)
  await loadRoutes(editId.value)
}

// ===== 跳转路径相关 =====
const redirects = ref<AppRedirect[]>([])
const showRedirectForm = ref(false)
const editingRedirectId = ref<number | null>(null)
const redirectFormError = ref('')
const redirectForm = ref({
  match_type: 'exact' as 'exact' | 'regex',
  match_path: '',
  match_include_query: false,
  redirect_target: '',
  status_code: 302,
  priority: 0,
})

function resetRedirectForm() {
  redirectForm.value = { match_type: 'exact', match_path: '', match_include_query: false, redirect_target: '', status_code: 302, priority: 0 }
  editingRedirectId.value = null
  redirectFormError.value = ''
}

function openCreateRedirect() {
  resetRedirectForm()
  showRedirectForm.value = true
}

function openEditRedirect(rd: AppRedirect) {
  editingRedirectId.value = rd.id
  redirectForm.value = {
    match_type: (rd.match_type || 'exact') as 'exact' | 'regex',
    match_path: rd.match_path || '',
    match_include_query: !!rd.match_include_query,
    redirect_target: rd.redirect_target || '',
    status_code: rd.status_code || 302,
    priority: rd.priority,
  }
  redirectFormError.value = ''
  showRedirectForm.value = true
}

async function loadRedirects(appId: number) {
  const res = await listAppRedirects(appId)
  if (res.data) redirects.value = res.data
}

async function handleSubmitRedirect() {
  const f = redirectForm.value
  if (!f.match_path.trim()) { redirectFormError.value = '匹配路径不能为空'; return }
  if (!f.redirect_target.trim()) { redirectFormError.value = '跳转目标不能为空'; return }

  const data = { ...f }
  redirectFormError.value = ''

  if (editingRedirectId.value !== null) {
    const res = await updateAppRedirect(editingRedirectId.value, data)
    if (res.error) { redirectFormError.value = res.error; return }
  } else {
    const res = await createAppRedirect(editId.value, data)
    if (res.error) { redirectFormError.value = res.error; return }
  }
  showRedirectForm.value = false
  await loadRedirects(editId.value)
}

async function handleDeleteRedirect(id: number) {
  if (!confirm('确定删除此跳转规则？')) return
  await deleteAppRedirect(id)
  await loadRedirects(editId.value)
}

// 路径豁免快捷选项
const exemptPathPresets = ['/favicon.ico', '/robots.txt', '/manifest.webmanifest', '/sitemap.xml']

// Cookie 过期时间选项
const cookieMaxAgeOptions = [
  { label: '会话（关闭浏览器即失效）', value: -1 },
  { label: '24 小时', value: 86400 },
  { label: '7 天', value: 604800 },
  { label: '30 天', value: 2592000 },
  { label: '一年', value: 31536000 },
  { label: '永久', value: 315360000 },
]

// BaseSelect 选项数组
const agentKeyOptions = computed(() => [
  { value: '', label: '无（直连）' },
  ...agentKeys.value.map(k => ({ value: k.uuid, label: `${k.name} (${k.uuid.slice(0, 8)})` })),
])

const authMethodOptions = [
  { value: 'none', label: '无 - 不需要认证' },
  { value: 'sso', label: '仅 SSO - 只支持 SSO 认证' },
  { value: 'sso_token', label: 'SSO + 票据 - 票据优先，SSO 回退' },
  { value: 'token', label: '仅票据 - 只允许使用访问票据' },
]

const headerModeOptions = [
  { value: 'auto_xff', label: '自动添加 X-Forwarded-*', description: '自动添加 X-Forwarded-Proto/Host/For，Origin 不处理（适合感知反向代理的后端）' },
  { value: 'auto_origin', label: '自动处理 Origin', description: '不自动添加 X-Forwarded-*，确保 Origin 和 Host 匹配（适合做 Origin 校验的后端）' },
  { value: 'none', label: '不自动处理', description: '不添加 X-Forwarded-* 也不改写 Origin，仅按自定义 Header 添加' },
]

const cookieMaxAgeSelectOptions = cookieMaxAgeOptions.map(o => ({ value: o.value, label: o.label }))

const routeMethodOptions = [
  { value: '', label: '任意' },
  { value: 'GET', label: 'GET' },
  { value: 'POST', label: 'POST' },
  { value: 'PUT', label: 'PUT' },
  { value: 'DELETE', label: 'DELETE' },
  { value: 'PATCH', label: 'PATCH' },
  { value: '*', label: '*（通配）' },
]

const routeAuthMethodOptions = [
  { value: '', label: '不覆盖（使用默认）' },
  { value: 'none', label: '无 - 不需要认证' },
  { value: 'sso', label: '仅 SSO - 只支持 SSO 认证' },
  { value: 'sso_token', label: 'SSO + 票据 - 票据优先' },
  { value: 'token', label: '仅票据 - 只允许访问票据' },
]

const redirectMatchTypeOptions = [
  { value: 'exact', label: '精确匹配' },
  { value: 'regex', label: '正则匹配' },
]

const redirectStatusCodeOptions = [
  { value: 302, label: '302 临时跳转（默认）' },
  { value: 301, label: '301 永久跳转' },
]

// 当前用户是否已启用 TOTP / 已注册通行密钥（用于判断二次验证可选方式）
const totpEnabled = ref(false)
const hasPasskey = ref(false)

// 是否显示应用二次验证设置：SSO 类认证 + 用户具备任一验证条件
const showSecondFactor = computed(() => {
  if (form.value.auth_method !== 'sso' && form.value.auth_method !== 'sso_token') return false
  return totpEnabled.value || hasPasskey.value
})

// 二次验证方式下拉选项（按用户实际具备的条件过滤）
const secondFactorOptions = computed(() => {
  const opts: Array<{ value: string; label: string }> = [{ value: '', label: '无' }]
  if (totpEnabled.value) opts.push({ value: 'totp', label: 'TOTP 验证码' })
  if (hasPasskey.value) opts.push({ value: 'passkey', label: '通行密钥' })
  return opts
})

function appURL(app: App): string {
  const base = `${window.location.protocol}//${app.subdomain}.${proxyDomain.value}`
  if (app.auth_method === 'token') {
    return base  // token 模式直接访问，由后端返回 401 提示
  }
  if (app.auth_method === 'sso' || app.require_auth) {
    return `/sso?redirect=${encodeURIComponent(base)}`
  }
  return base
}

// 应用可用性状态显示（融合客户端在线状态 + 探测结果）
// 返回 { text, colorClass, title? } 用于 UI 渲染
//   text: 简短文案；错误带状态码（错误(502)），失败按原因分类（连接失败/连接被拒/解析失败/连接断开）
//   title: 鼠标悬停时显示的完整原因（仅 failed 时填充）
function appAvailability(app: App): { text: string; colorClass: string; title?: string } {
  if (!app.enabled) return { text: '已禁用', colorClass: 'text-gray-400' }
  if (!app.client_ids?.length) return { text: '未关联', colorClass: 'text-yellow-600' }
  if (!app.client_online) return { text: '离线', colorClass: 'text-gray-400' }
  // 客户端在线，看探测状态
  const ps = app.probe_status
  if (!ps) return { text: '可用', colorClass: 'text-green-600' } // 未探测过，默认可用
  switch (ps.status) {
    case 'available': return { text: '可用', colorClass: 'text-green-600' }
    case 'error':    return { text: `错误(${ps.status_code})`, colorClass: 'text-red-600' }
    case 'failed':   return { text: failedReason(ps.detail), colorClass: 'text-red-600', title: ps.detail }
    case 'timeout':  return { text: '超时', colorClass: 'text-red-600' }
    case 'offline':  return { text: '离线', colorClass: 'text-gray-400' }
    default:         return { text: '可用', colorClass: 'text-green-600' }
  }
}

// 基于 detail 关键词推断简短失败原因（连接类错误的典型分类）
function failedReason(detail?: string): string {
  if (!detail) return '连接失败'
  const lower = detail.toLowerCase()
  if (lower.includes('refused')) return '连接被拒'
  if (lower.includes('no such host')) return '解析失败'
  if (lower.includes('reset') || lower.includes('eof')) return '连接断开'
  return '连接失败'
}

function clientName(id: string): string {
  if (id === '__host__') return 'Host'
  return clientList.value.find(c => c.id === id)?.name || id
}

function toggleClient(id: string) {
  const idx = form.value.client_ids.indexOf(id)
  if (idx >= 0) {
    form.value.client_ids.splice(idx, 1)
    // 删除对应的配置
    delete form.value.client_configs[id]
    // 取消多选时若只剩一个，关闭负载均衡
    if (form.value.client_ids.length < 2) form.value.load_balance = false
  } else {
    // 新增客户端：若是 __host__，先删除已有的 __host__（只保留最后一个）
    if (id === '__host__') {
      const hostIdx = form.value.client_ids.indexOf('__host__')
      if (hostIdx >= 0) {
        form.value.client_ids.splice(hostIdx, 1)
        delete form.value.client_configs['__host__']
      }
    }
    form.value.client_ids.push(id)
    // 添加默认配置
    form.value.client_configs[id] = emptyClientConfig()
    // 加载该客户端的代理列表
    if (id !== '__host__') {
      loadClientProxies(id)
    }
  }
}

function moveUp(idx: number) {
  if (idx === 0) return
  const arr = form.value.client_ids
  ;[arr[idx - 1], arr[idx]] = [arr[idx], arr[idx - 1]]
}

function moveDown(idx: number) {
  const arr = form.value.client_ids
  if (idx >= arr.length - 1) return
  ;[arr[idx], arr[idx + 1]] = [arr[idx + 1], arr[idx]]
}

// 创建模式下：名称变化时自动推导子域名（仅子域名未被人工修改时）
watch(() => form.value.name, (name) => {
  if (formMode.value === 'create' && !subdomainManuallyEdited.value) {
    form.value.subdomain = deriveSubdomain(name)
  }
})

// 自定义 Header 相关函数
function addHeader() {
  const key = `Header-${Object.keys(form.value.custom_headers).length + 1}`
  form.value.custom_headers[key] = ''
}

function removeHeader(key: string) {
  delete form.value.custom_headers[key]
}

function updateHeaderKey(oldKey: string, newKey: string) {
  if (oldKey === newKey) return
  const value = form.value.custom_headers[oldKey]
  delete form.value.custom_headers[oldKey]
  form.value.custom_headers[newKey] = value
}

// 路径豁免相关函数
function addExemptPath(path: string) {
  form.value.exempt_paths.push(path)
}

function removeExemptPath(idx: number) {
  form.value.exempt_paths.splice(idx, 1)
}

// ===== 应用排序 =====
// 表头点击排序；默认按「最近使用」倒序（最近在最前）
type SortKey = 'name' | 'subdomain' | 'target_url' | 'client' | 'auth_method' | 'status' | 'last_used'
const sortKey = ref<SortKey>('last_used')
const sortDir = ref<'asc' | 'desc'>('desc')

function lastUsedTs(a: App): number {
  return a.last_used_at ? new Date(a.last_used_at).getTime() : 0
}

// 状态映射为可比较的优先级数值：
// 未关联(0) < 离线(1) < 失败/超时/错误(2) < 可用(3) < 未探测(3，默认可用)
function statusCode(a: App): number {
  if (!a.enabled) return -1 // 已禁用排最前
  if (!a.client_ids?.length) return 0
  if (!a.client_online) return 1
  const ps = a.probe_status
  if (!ps) return 3 // 未探测过，默认可用
  switch (ps.status) {
    case 'available': return 3
    case 'error':    return 2
    case 'failed':   return 2
    case 'timeout':  return 2
    case 'offline':  return 1
    default:         return 3
  }
}

// 客户端列：取主客户端名称（client_infos[0]），无则空串
function clientLabel(a: App): string {
  return a.client_infos?.[0]?.client_name || ''
}

function compareValues(a: App, b: App, key: SortKey): number {
  let av: string | number
  let bv: string | number
  switch (key) {
    case 'name': av = a.name.toLowerCase(); bv = b.name.toLowerCase(); break
    case 'subdomain': av = a.subdomain.toLowerCase(); bv = b.subdomain.toLowerCase(); break
    case 'target_url': av = a.target_url.toLowerCase(); bv = b.target_url.toLowerCase(); break
    case 'client': av = clientLabel(a).toLowerCase(); bv = clientLabel(b).toLowerCase(); break
    case 'auth_method': av = a.auth_method || ''; bv = b.auth_method || ''; break
    case 'status': av = statusCode(a); bv = statusCode(b); break
    case 'last_used': av = lastUsedTs(a); bv = lastUsedTs(b); break
  }
  if (av < bv) return -1
  if (av > bv) return 1
  return 0
}

const sortedApps = computed(() => {
  const list = [...filteredApps.value]
  list.sort((a, b) => {
    const cmp = compareValues(a, b, sortKey.value)
    return sortDir.value === 'asc' ? cmp : -cmp
  })
  return list
})

function toggleSort(key: SortKey) {
  if (sortKey.value === key) {
    sortDir.value = sortDir.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortKey.value = key
    // last_used 默认倒序（最近在前），其它列默认正序
    sortDir.value = key === 'last_used' ? 'desc' : 'asc'
  }
}

// 表头排序指示符：激活列显示方向箭头，非激活列显示淡的 ⇅
function sortIndicator(k: SortKey): string {
  if (sortKey.value !== k) return '⇅'
  return sortDir.value === 'asc' ? '↑' : '↓'
}

function sortIndicatorClass(k: SortKey): string {
  return sortKey.value === k ? 'text-blue-600' : 'text-gray-300'
}

// ===== 表头筛选（#59，交互模式同系统日志页）=====
// 文本列（名称/子域名/目标地址）：关键词包含匹配
// 枚举列（客户端/认证/状态）：复选多选
type FilterField = 'name' | 'subdomain' | 'target_url' | 'client' | 'auth_method' | 'status'
const filterFields: FilterField[] = ['name', 'subdomain', 'target_url', 'client', 'auth_method', 'status']
const textFilterFields: FilterField[] = ['name', 'subdomain', 'target_url']

const filters = reactive({
  name: '',
  subdomain: '',
  target_url: '',
  client: [] as string[],
  auth_method: [] as string[],
  status: [] as string[],
})

// 筛选弹层状态
const filterPopover = reactive<{ open: boolean; field: FilterField; x: number; y: number }>({
  open: false,
  field: 'name',
  x: 0,
  y: 0,
})
const filterSearch = ref('')
const localSelection = ref<string[]>([])
const filterText = ref('')

// 弹层定位（靠边时收进视口内）
const popoverStyle = computed(() => {
  const x = Math.min(filterPopover.x, window.innerWidth - 220)
  const y = Math.min(filterPopover.y, window.innerHeight - 320)
  return { left: x + 'px', top: y + 'px' }
})

const filterMode = computed<'text' | 'list'>(() =>
  textFilterFields.includes(filterPopover.field) ? 'text' : 'list'
)

// 归一化认证方式（与列表展示口径一致：sso 或遗留 require_auth 均算 SSO）
function authCategory(a: App): string {
  if (a.auth_method === 'token') return 'token'
  if (a.auth_method === 'sso_token') return 'sso_token'
  if (a.auth_method === 'sso' || a.require_auth) return 'sso'
  return 'none'
}

// 状态归一化 key（与 appAvailability 口径一致，failed 细分合并为「失败」）
function statusKeyOf(a: App): string {
  if (!a.enabled) return 'disabled'
  if (!a.client_ids?.length) return 'unlinked'
  if (!a.client_online) return 'offline'
  const ps = a.probe_status
  if (!ps) return 'available'
  switch (ps.status) {
    case 'available': return 'available'
    case 'error':    return 'error'
    case 'failed':   return 'failed'
    case 'timeout':  return 'timeout'
    case 'offline':  return 'offline'
    default:         return 'available'
  }
}

// 枚举列的可选项（客户端从当前应用列表聚合，认证/状态为固定枚举）
const filterOptions = computed<{ value: string; label: string }[]>(() => {
  switch (filterPopover.field) {
    case 'client': {
      const seen = new Map<string, string>()
      for (const a of apps.value) {
        for (const ci of a.client_infos || []) {
          if (!seen.has(ci.client_id)) {
            seen.set(ci.client_id, ci.client_id === '__host__' ? 'Host' : (ci.client_name || ci.client_id))
          }
        }
      }
      return Array.from(seen, ([value, label]) => ({ value, label })).sort((x, y) => x.label.localeCompare(y.label))
    }
    case 'auth_method':
      return [
        { value: 'none', label: '无' },
        { value: 'sso', label: 'SSO' },
        { value: 'sso_token', label: 'SSO+票据' },
        { value: 'token', label: '票据' },
      ]
    case 'status':
      return [
        { value: 'available', label: '可用' },
        { value: 'offline', label: '离线' },
        { value: 'unlinked', label: '未关联' },
        { value: 'error', label: '错误' },
        { value: 'failed', label: '失败' },
        { value: 'timeout', label: '超时' },
        { value: 'disabled', label: '已禁用' },
      ]
    default:
      return []
  }
})

const filteredFilterOptions = computed(() => {
  if (!filterSearch.value) return filterOptions.value
  const q = filterSearch.value.toLowerCase()
  return filterOptions.value.filter(o => o.label.toLowerCase().includes(q))
})

function isFilterActive(field: FilterField): boolean {
  const v = (filters as any)[field]
  return Array.isArray(v) ? v.length > 0 : !!v
}

const activeFilterCount = computed(() => filterFields.filter(f => isFilterActive(f)).length)

function openFilter(evt: MouseEvent, field: FilterField) {
  filterPopover.field = field
  filterPopover.x = evt.clientX
  filterPopover.y = evt.clientY + 20
  // 复制当前已选到本地编辑区
  if (textFilterFields.includes(field)) {
    filterText.value = (filters as any)[field]
  } else {
    localSelection.value = [...((filters as any)[field] as string[])]
  }
  filterSearch.value = ''
  filterPopover.open = true
}

function closeFilter() {
  filterPopover.open = false
}

function toggleSelect(v: string) {
  const i = localSelection.value.indexOf(v)
  if (i >= 0) localSelection.value.splice(i, 1)
  else localSelection.value.push(v)
}

function applyFilter() {
  const field = filterPopover.field
  if (textFilterFields.includes(field)) {
    ;(filters as any)[field] = filterText.value.trim()
  } else {
    ;(filters as any)[field] = [...localSelection.value]
  }
  filterPopover.open = false
}

function clearFilter() {
  const field = filterPopover.field
  ;(filters as any)[field] = textFilterFields.includes(field) ? '' : []
  filterPopover.open = false
}

function resetAllFilters() {
  filters.name = ''
  filters.subdomain = ''
  filters.target_url = ''
  filters.client = []
  filters.auth_method = []
  filters.status = []
}

// 应用筛选：所有列条件取交集
const filteredApps = computed(() => {
  const q = (s: string) => s.trim().toLowerCase()
  return apps.value.filter(a => {
    if (filters.name && !a.name.toLowerCase().includes(q(filters.name))) return false
    if (filters.subdomain && !a.subdomain.toLowerCase().includes(q(filters.subdomain))) return false
    if (filters.target_url && !(a.target_url || '').toLowerCase().includes(q(filters.target_url))) return false
    if (filters.client.length > 0 && !a.client_infos?.some(ci => filters.client.includes(ci.client_id))) return false
    if (filters.auth_method.length > 0 && !filters.auth_method.includes(authCategory(a))) return false
    if (filters.status.length > 0 && !filters.status.includes(statusKeyOf(a))) return false
    return true
  })
})

async function loadApps() {
  const res = await listApps(viewingUserId.value || undefined)
  if (res.error) {
    if (res.error.includes('无权')) {
      forbidden.value = true
      apps.value = []
    }
    return
  }
  if (res.data) {
    apps.value = res.data
  }
}

async function loadClients() {
  const res = await listClients()
  if (res.data) clientList.value = res.data.filter(c => c.id !== '__host__')
}

// 加载客户端的代理列表
async function loadClientProxies(clientId: string) {
  if (clientProxies.value[clientId]) return  // 已缓存
  const res = await listProxies(clientId)
  if (res.data) {
    clientProxies.value[clientId] = res.data
  }
}

function openCreate() {
  formMode.value = 'create'
  form.value = { name: '', subdomain: '', client_ids: [], client_configs: {}, auth_method: 'sso', allowed_users: 'owner', sso_cookie_max_age: 86400, load_balance: false, custom_headers: { 'Host': '${host}' }, header_mode: 'auto_xff', proxy_config: {}, exempt_paths: [], agent_key_uuid: '', second_factor: '' }
  targetSchema.value = 'http'
  targetHost.value = ''
  targetPort.value = ''
  formError.value = ''
  advancedOpen.value = false
  subdomainManuallyEdited.value = false
  showForm.value = true
  clientProxies.value = {}  // 清空代理缓存
  loadClients().then(() => {
    // 仅有一个远程客户端时自动预选，降低输入成本
    if (clientList.value.length === 1 && form.value.client_ids.length === 0) {
      toggleClient(clientList.value[0].id)
    }
  })
  // 创建模式下不加载路由规则（还没有 app ID）
}

// 从 app 对象填充表单（openEdit 用）
function populateFormFromApp(app: App) {
  formMode.value = 'edit'
  editId.value = app.id

  // 普通用户编辑时去掉前缀（前缀由输入框左侧固定显示）
  let subdomain = app.subdomain
  if (!isAdmin.value && subdomain.startsWith(subdomainPrefix.value)) {
    subdomain = subdomain.slice(subdomainPrefix.value.length)
  }

  // 构建 client_configs
  const clientConfigs: Record<string, ClientConfig> = {}
  if (app.client_infos) {
    for (const info of app.client_infos) {
      clientConfigs[info.client_id] = {
        target_url: info.target_url || '',
        proxy_id: info.proxy_id || null,
      }
    }
  }
  // 兼容旧数据：如果 auth_method 是旧的 sso_owner/sso_all，转换为新的 sso
  let authMethod = app.auth_method || 'sso'
  if (authMethod === 'sso_owner' || authMethod === 'sso_all') {
    authMethod = 'sso'
  }
  // 兼容旧数据：根据旧的 auth_method 推断 allowed_users
  let allowedUsers = app.allowed_users || 'owner'
  if (!app.allowed_users && app.auth_method === 'sso_all') {
    allowedUsers = 'all'
  }

  form.value = {
    name: app.name,
    subdomain: subdomain,
    client_ids: [...(app.client_ids || [])],
    client_configs: clientConfigs,
    auth_method: authMethod as 'none' | 'sso' | 'sso_token' | 'token',
    allowed_users: allowedUsers as 'owner' | 'all',
    sso_cookie_max_age: app.sso_cookie_max_age || 86400,
    load_balance: app.load_balance || false,
    custom_headers: app.custom_headers ? { ...app.custom_headers } : {},
    header_mode: (app.header_mode || 'auto_xff') as 'auto_xff' | 'auto_origin' | 'none',
    proxy_config: parseProxyConfigText(app.proxy_config || ''),
    exempt_paths: app.exempt_paths ? [...app.exempt_paths] : [],
    agent_key_uuid: app.agent_key_uuid || '',
    second_factor: (app.second_factor || '') as '' | 'totp' | 'passkey',
  }
  // 目标地址反向解析为三段回填
  const parsed = parseTargetUrl(app.target_url || '')
  targetSchema.value = parsed.schema
  targetHost.value = parsed.host
  targetPort.value = parsed.port
  formError.value = ''
  showForm.value = true
  clientProxies.value = {}  // 清空代理缓存
  loadClients()
  // 加载已选客户端的代理列表
  for (const cid of app.client_ids || []) {
    if (cid !== '__host__') {
      loadClientProxies(cid)
    }
  }
  // 加载路由规则与跳转规则
  if (app.id) {
    routes.value = []
    showRouteForm.value = false
    resetRouteForm()
    loadRoutes(app.id)
    redirects.value = []
    showRedirectForm.value = false
    resetRedirectForm()
    loadRedirects(app.id)
  } else {
    routes.value = []
    redirects.value = []
  }
}

function openEdit(app: App) {
  populateFormFromApp(app)
}

// 克隆应用：后端静默完成全部配置复制（含路由/跳转规则），成功后打开新应用的编辑页
async function handleClone(app: App) {
  const res = await duplicateApp(app.id)
  if (res.error) {
    alert(`克隆失败：${res.error}`)
    return
  }
  await loadApps()
  if (res.data) {
    openEdit(res.data)
  }
}

async function handleSubmit() {
  if (form.value.client_ids.length === 0) {
    formError.value = '请至少选择一个客户端'
    return
  }
  formError.value = ''

  // 构建提交数据，清理空值
  const submitData = {
    ...form.value,
    target_url: assembleTargetUrl(),
    client_configs: {} as Record<string, { target_url?: string; proxy_id?: number | null }>,
    // 应用级代理配置覆盖序列化为多行 key: value 文本（#47）
    proxy_config: formatProxyConfigText(form.value.proxy_config, appProxyKeys.value),
  }

  for (const cid of form.value.client_ids) {
    const cfg = form.value.client_configs[cid] || emptyClientConfig()
    const submitCfg: { target_url?: string; proxy_id?: number | null } = {}
    if (cfg.target_url) submitCfg.target_url = cfg.target_url
    if (cfg.proxy_id !== null && cfg.proxy_id !== undefined) submitCfg.proxy_id = cfg.proxy_id
    if (Object.keys(submitCfg).length > 0) {
      submitData.client_configs[cid] = submitCfg
    }
  }

  if (formMode.value === 'create') {
    const res = await createApp(submitData)
    if (res.error) { formError.value = res.error; return }
  } else {
    const res = await updateApp(editId.value, submitData)
    if (res.error) { formError.value = res.error; return }
  }
  showForm.value = false
  await loadApps()
}

async function handleDelete(id: number) {
  if (!confirm('确定删除此应用？')) return
  await deleteApp(id)
  await loadApps()
}

async function toggleEnabled(app: App) {
  // 构建 client_configs
  const clientConfigs: Record<string, { target_url?: string; proxy_id?: number | null }> = {}
  if (app.client_infos) {
    for (const info of app.client_infos) {
      const cfg: { target_url?: string; proxy_id?: number | null } = {}
      if (info.target_url) cfg.target_url = info.target_url
      if (info.proxy_id) cfg.proxy_id = info.proxy_id
      if (Object.keys(cfg).length > 0) {
        clientConfigs[info.client_id] = cfg
      }
    }
  }
  // 兼容旧数据
  let authMethod = app.auth_method || 'sso'
  if (authMethod === 'sso_owner' || authMethod === 'sso_all') {
    authMethod = 'sso'
  }
  let allowedUsers = app.allowed_users || 'owner'
  if (!app.allowed_users && app.auth_method === 'sso_all') {
    allowedUsers = 'all'
  }
  await updateApp(app.id, {
    name: app.name,
    subdomain: app.subdomain,
    target_url: app.target_url,
    client_ids: app.client_ids || [],
    client_configs: clientConfigs,
    enabled: !app.enabled,
    auth_method: authMethod,
    allowed_users: allowedUsers,
    sso_cookie_max_age: app.sso_cookie_max_age || 86400,
    custom_headers: app.custom_headers || {},
    header_mode: app.header_mode || 'auto_xff',
    exempt_paths: app.exempt_paths || [],
    agent_key_uuid: app.agent_key_uuid || '',
    second_factor: app.second_factor || '',
  })
  await loadApps()
}

function exitUserView() {
  router.push('/apps')
}

// ===== 分享码相关 =====
const showShareForm = ref(false)
const shareApp = ref<App | null>(null)
const shareFormError = ref('')
const newShareURL = ref('')
const shareCopied = ref(false)
const shareForm = ref({
  expires_in_secs: 86400,
  cookie_ttl: 604800,
  max_uses: 3,
  redirect_path: '',
  concrete_subdomain: '',
})

// 判断子域名是否为模糊匹配模式（与服务端 subdomain.IsFuzzy 一致）
function isFuzzySubdomain(subdomain: string): boolean {
  return subdomain.includes('*') || subdomain.includes('/')
}

const shareExpiryOptions = [
  { label: '1 小时', value: 3600 },
  { label: '24 小时（默认）', value: 86400 },
  { label: '3 天', value: 259200 },
  { label: '7 天', value: 604800 },
  { label: '30 天', value: 2592000 },
  { label: '一年', value: 31536000 },
  { label: '永久', value: 315360000 },
]

const shareCookieTTLOptions = [
  { label: '24 小时', value: 86400 },
  { label: '7 天（默认）', value: 604800 },
  { label: '30 天', value: 2592000 },
  { label: '一年', value: 31536000 },
  { label: '永久', value: 315360000 },
]

// BaseSelect 用选项数组
const shareExpirySelectOptions = shareExpiryOptions.map(o => ({ value: o.value, label: o.label }))
const shareCookieTTLSelectOptions = shareCookieTTLOptions.map(o => ({ value: o.value, label: o.label }))
const routeClientOptions = computed(() => [
  { value: '', label: '任意' },
  ...form.value.client_ids.map((cid: string) => ({ value: cid, label: clientName(cid) })),
])

function canShare(app: App): boolean {
  return app.auth_method === 'sso' || app.auth_method === 'sso_token'
}

function openShare(app: App) {
  shareApp.value = app
  shareForm.value = { expires_in_secs: 86400, cookie_ttl: 604800, max_uses: 3, redirect_path: '', concrete_subdomain: '' }
  shareFormError.value = ''
  showShareForm.value = true
}

async function handleShareSubmit() {
  if (!shareApp.value) return
  shareFormError.value = ''
  if (shareForm.value.max_uses < 1) {
    shareFormError.value = '跳转次数至少为 1'
    return
  }
  // 跳转路径校验：留空表示 /；非空必须以 / 开头，且不能是 // 或 /\
  const path = (shareForm.value.redirect_path || '').trim()
  if (path !== '' && !path.startsWith('/')) {
    shareFormError.value = '跳转路径必须以 / 开头'
    return
  }
  if (path.startsWith('//') || path.startsWith('/\\')) {
    shareFormError.value = '跳转路径格式非法'
    return
  }
  // 模糊匹配应用的具体子域名校验
  const concreteSub = (shareForm.value.concrete_subdomain || '').trim().toLowerCase()
  if (isFuzzySubdomain(shareApp.value.subdomain)) {
    if (!concreteSub) {
      shareFormError.value = '模糊匹配应用需填写具体子域名'
      return
    }
  }
  const res = await createShareCode({
    app_id: shareApp.value.id,
    max_uses: shareForm.value.max_uses,
    cookie_ttl: shareForm.value.cookie_ttl,
    expires_in_secs: shareForm.value.expires_in_secs,
    redirect_path: path,
    concrete_subdomain: concreteSub,
  })
  if (res.error) { shareFormError.value = res.error; return }
  showShareForm.value = false
  if (res.data) {
    newShareURL.value = `${window.location.origin}/s/${res.data.code}`
  }
}

async function copyNewShareURL() {
  try {
    await navigator.clipboard.writeText(newShareURL.value)
    shareCopied.value = true
    setTimeout(() => { shareCopied.value = false }, 2000)
  } catch {
    // ignore
  }
}

onMounted(async () => {
  // 检查是否有 user_id 参数
  const userIdParam = route.query.user_id
  if (userIdParam) {
    const uid = Number(userIdParam)
    if (!isNaN(uid)) {
      if (!isAdmin.value) {
        forbidden.value = true
      } else {
        viewingUserId.value = uid
        // 加载用户列表以获取用户名
        const usersRes = await listUsers()
        if (usersRes.data) {
          users.value = usersRes.data
          const user = usersRes.data.find(u => u.id === uid)
          if (user) {
            viewingUsername.value = user.username
          }
        }
      }
    }
  }

  const [, settingsRes] = await Promise.all([loadApps(), getSettings()])
  loading.value = false
  if (settingsRes.data) proxyDomain.value = settingsRes.data.proxy_domain || ''
  // 加载安全代理密钥列表
  const keysRes = await listAgentKeys()
  if (keysRes.data) agentKeys.value = keysRes.data
  // 加载应用级代理配置可覆盖 key 元数据（#47）
  loadAppProxyKeys()
  // 加载当前用户 TOTP / 通行密钥状态，用于判断应用二次验证可选方式 (#76)
  const [userSettingsRes, credentialsRes] = await Promise.all([getUserSettings(), listCredentials()])
  if (userSettingsRes.data) totpEnabled.value = userSettingsRes.data.totp_enabled
  if (credentialsRes.data) hasPasskey.value = credentialsRes.data.length > 0
})
</script>
