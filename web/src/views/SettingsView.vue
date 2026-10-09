<template>
  <div>
    <h2 class="text-lg md:text-xl font-bold mb-4">个人设置</h2>

    <div class="max-w-lg space-y-6">
      <!-- 自动禁用天数 -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">自动禁用天数</h3>
        <p class="text-sm text-gray-600 mb-4">
          设置应用不活跃后自动禁用的天数。启用后，您的所有应用若超过指定天数未被访问，将自动禁用。
        </p>
        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="settingsLoading" />
        <div v-else>
          <div class="flex items-center gap-2">
            <span class="text-sm text-gray-500">超过</span>
            <input v-model.number="autoDisableDays" type="number" min="0" max="365"
              class="w-20 px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
              placeholder="0" />
            <span class="text-sm text-gray-500">天未使用自动禁用（0 表示禁用此功能）</span>
          </div>
          <div v-if="autoDisableMsg" class="text-sm mt-3 px-3 py-2 rounded-md"
            :class="autoDisableError ? 'text-red-600 bg-red-50' : 'text-green-600 bg-green-50'">
            {{ autoDisableMsg }}
          </div>
          <button @click="handleUpdateAutoDisable" :disabled="autoDisableLoading"
            class="mt-4 px-4 py-2 bg-gray-800 text-white text-sm rounded-md hover:bg-gray-900 disabled:opacity-50">
            {{ autoDisableLoading ? '保存中...' : '保存' }}
          </button>
        </div>
        </Transition>
      </div>

      <!-- 快速登录 -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">快速登录</h3>
        <p class="text-sm text-gray-600 mb-4">
          启用后，访问开启 SSO 认证的应用时无需在 SSO 页面点「确认访问」——
          只要管理端会话仍有效，SSO Cookie 过期或缺失时将自动下发新 Cookie 并跳回应用。
          适合单用户场景，多用户环境请按需评估。
        </p>
        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="settingsLoading" />
        <div v-else>
          <div class="flex items-center gap-3">
            <label class="inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="quickLogin"
                class="sr-only peer" />
              <div class="w-11 h-6 bg-gray-200 rounded-full peer peer-checked:bg-blue-600 peer-focus:ring-2 peer-focus:ring-blue-500 transition-colors relative after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-transform peer-checked:after:translate-x-5"></div>
              <span class="ml-3 text-sm text-gray-700">{{ quickLogin ? '已启用' : '未启用' }}</span>
            </label>
            <button @click="handleUpdateQuickLogin" :disabled="quickLoginLoading"
              class="px-4 py-2 bg-gray-800 text-white text-sm rounded-md hover:bg-gray-900 disabled:opacity-50">
              {{ quickLoginLoading ? '保存中...' : '保存' }}
            </button>
          </div>
          <div v-if="quickLoginMsg" class="text-sm mt-3 px-3 py-2 rounded-md"
            :class="quickLoginError ? 'text-red-600 bg-red-50' : 'text-green-600 bg-green-50'">
            {{ quickLoginMsg }}
          </div>
        </div>
        </Transition>
      </div>

      <!-- 应用 API（外部 token 调用 /external-api/，用于导航页等外部场景） -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">应用 API</h3>
        <p class="text-sm text-gray-600 mb-4">
          启用后会生成独立 token，用于调用 <code class="bg-gray-100 px-1 rounded">/external-api/apps</code> 接口获取应用列表（只读），可对接 linkding start page 等导航页面。
          关闭将立即清空 token，重置则旧 token 立即失效。
        </p>
        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="settingsLoading" />
        <div v-else>
          <div class="flex items-center gap-3 mb-4">
            <label class="inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="appApiEnabled"
                class="sr-only peer" />
              <div class="w-11 h-6 bg-gray-200 rounded-full peer peer-checked:bg-blue-600 peer-focus:ring-2 peer-focus:ring-blue-500 transition-colors relative after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-transform peer-checked:after:translate-x-5"></div>
              <span class="ml-3 text-sm text-gray-700">{{ appApiEnabled ? '已启用' : '未启用' }}</span>
            </label>
            <button @click="handleToggleAppApi" :disabled="appApiLoading"
              class="px-4 py-2 bg-gray-800 text-white text-sm rounded-md hover:bg-gray-900 disabled:opacity-50">
              {{ appApiLoading ? '保存中...' : '保存' }}
            </button>
          </div>

          <!-- token 展示与重置 -->
          <div v-if="appApiEnabled && appApiTokenSet" class="space-y-3">
            <div>
              <label class="block text-xs text-gray-500 mb-1">应用 API Token</label>
              <div class="flex items-center gap-2">
                <code class="flex-1 text-xs bg-gray-100 px-2 py-1.5 rounded break-all select-all">{{ appApiToken }}</code>
                <button @click="copyAppApiToken" type="button"
                  class="px-3 py-1.5 text-xs bg-gray-100 text-gray-700 rounded hover:bg-gray-200">
                  {{ appApiCopied ? '✓' : '复制' }}
                </button>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <button @click="showAppApiResetModal = true" type="button"
                class="px-3 py-1.5 text-sm bg-amber-600 border border-amber-300 rounded-md hover:bg-amber-50">
                重置 Token
              </button>
              <span class="text-xs text-gray-400">重置后旧 token 立即失效</span>
            </div>
          </div>

          <div v-if="appApiMsg" class="text-sm mt-3 px-3 py-2 rounded-md"
            :class="appApiError ? 'text-red-600 bg-red-50' : 'text-green-600 bg-green-50'">
            {{ appApiMsg }}
          </div>
        </div>
        </Transition>
      </div>

      <!-- 修改密码 -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">修改密码</h3>
        <form @submit.prevent="handleChangePassword" class="space-y-3">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">旧密码</label>
            <input v-model="pwForm.oldPassword" type="password" required
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">新密码</label>
            <input v-model="pwForm.newPassword" type="password" required minlength="6"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div v-if="pwMsg" class="text-sm px-3 py-2 rounded-md"
            :class="pwError ? 'text-red-600 bg-red-50' : 'text-green-600 bg-green-50'">
            {{ pwMsg }}
          </div>
          <button type="submit" class="px-4 py-2 bg-gray-800 text-white text-sm rounded-md hover:bg-gray-900">
            修改密码
          </button>
        </form>
      </div>

      <!-- TOTP 密钥（独立基础功能，二次验证与临时登录均依赖此密钥） -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">TOTP 密钥</h3>
        <p class="text-sm text-gray-600 mb-4">
          绑定 Google Authenticator 等认证器的密钥，是二次验证与临时登录的基础。
          密钥一旦生成即持久保留，重置需重新扫码绑定。
        </p>

        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="settingsLoading" />

        <div v-else>
          <!-- 未绑定 -->
          <div v-if="!totpSecretSet">
            <button @click="handleBindTOTP" :disabled="bindLoading"
              class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
              {{ bindLoading ? '生成中...' : '绑定密钥' }}
            </button>
          </div>

          <!-- 已绑定 -->
          <div v-else class="flex items-center gap-3">
            <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
              已绑定
            </span>
            <button @click="showResetModal = true" class="text-sm text-amber-600 hover:text-amber-700">
              重置密钥
            </button>
          </div>
        </div>
        </Transition>
      </div>

      <!-- 二次验证（依赖 TOTP 密钥） -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">二次验证</h3>
        <p class="text-sm text-gray-600 mb-4">
          启用后，登录管理后台时需要输入认证器生成的验证码，提高账户安全性。
        </p>

        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="settingsLoading" />

        <div v-else>
          <!-- 未绑定密钥：提示先绑定 -->
          <div v-if="!totpSecretSet">
            <p class="text-xs text-amber-600 bg-amber-50 px-3 py-2 rounded-md">
              请先绑定 TOTP 密钥
            </p>
          </div>

          <!-- 已绑定密钥 -->
          <template v-else>
            <!-- 已启用 -->
            <div class="flex items-center gap-3">
              <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                已启用
              </span>
              <button @click="showDisableModal = true" class="text-sm text-red-600 hover:text-red-700">
                禁用
              </button>
            </div>

            <!-- 未启用 -->
            <div v-if="!totpEnabled">
              <button @click="showEnableModal = true"
                class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
                启用二次验证
              </button>
            </div>
          </template>
        </div>
        </Transition>
      </div>

      <!-- 临时登录（依赖 TOTP 密钥） -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">临时登录</h3>
        <p class="text-sm text-gray-600 mb-4">
          启用后，可在登录页通过 用户名 + PIN + TOTP 验证码 一次性换取某个应用的访问权限，不登录管理后台。
          每个验证码 2 分钟内仅可使用一次。
        </p>

        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="settingsLoading" />

        <div v-else>
          <!-- 未绑定密钥：提示先绑定 -->
          <div v-if="!totpSecretSet">
            <p class="text-xs text-amber-600 bg-amber-50 px-3 py-2 rounded-md">
              请先绑定 TOTP 密钥
            </p>
          </div>

          <!-- 已绑定密钥 -->
          <template v-else>
            <!-- 已启用 -->
            <div v-if="tempLoginEnabled" class="flex items-center gap-3">
              <span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium bg-green-100 text-green-800">
                已启用
              </span>
              <button @click="showTempPINModal = true" class="text-sm text-blue-600 hover:text-blue-700">
                修改 PIN
              </button>
              <button @click="handleDisableTempLogin" class="text-sm text-red-600 hover:text-red-700">
                关闭
              </button>
            </div>

            <!-- 未启用 -->
            <div v-else>
              <button @click="showTempSetupModal = true"
                class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
                启用临时登录
              </button>
            </div>
          </template>
        </div>
        </Transition>
      </div>

      <!-- 通行密钥（#71：Face ID / Touch ID 登录管理后台） -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">通行密钥</h3>
        <p class="text-sm text-gray-600 mb-4">
          注册后可使用通行密钥登录管理后台：设备会通过生物识别（Face ID / Touch ID / 指纹等）或系统弹窗完成验证。
          凭据可经 iCloud 钥匙串等多设备同步。注册时需输入 TOTP 验证码做再验证；扫码授权只需本机确认，无需验证码。
        </p>

        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="passkeyLoading" />

        <div v-else>
          <!-- 浏览器不支持 -->
          <p v-if="!passkeySupported" class="text-xs text-amber-600 bg-amber-50 px-3 py-2 rounded-md">
            当前浏览器不支持通行密钥，请使用 Safari / Chrome 等现代浏览器
          </p>

          <template v-else>
            <!-- 未绑定 TOTP：提示先绑定 -->
            <p v-if="!totpSecretSet" class="text-xs text-amber-600 bg-amber-50 px-3 py-2 rounded-md">
              请先绑定 TOTP 密钥
            </p>

            <template v-else>
              <!-- 凭据列表 -->
              <div v-if="credentials.length > 0" class="space-y-2 mb-4">
                <div v-for="cred in credentials" :key="cred.id"
                  class="flex items-center justify-between bg-gray-50 rounded-md px-3 py-2">
                  <div class="min-w-0">
                    <p class="text-sm font-medium text-gray-800 truncate">{{ cred.device_name || '未命名设备' }}</p>
                    <p class="text-xs text-gray-400">
                      {{ cred.backup_eligible ? 'iCloud 同步' : '本机存储' }} ·
                      注册于 {{ formatDate(cred.created_at) }}
                      <template v-if="cred.last_used_at"> · 上次使用 {{ formatDate(cred.last_used_at) }}</template>
                    </p>
                  </div>
                  <div class="flex items-center gap-2 shrink-0 ml-2">
                    <button @click="openRenameModal(cred)" class="text-sm text-blue-600 hover:text-blue-700">改名</button>
                    <button @click="handleDeleteCredential(cred)" class="text-sm text-red-600 hover:text-red-700">删除</button>
                  </div>
                </div>
              </div>
              <p v-else class="text-xs text-gray-400 mb-4">尚未注册任何通行密钥</p>

              <button @click="showRegisterModal = true" :disabled="registerLoading"
                class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
                注册通行密钥
              </button>
            </template>
          </template>
        </div>
        </Transition>
      </div>
    </div>

    <!-- 绑定 TOTP 密钥弹窗（扫码） -->
    <div v-if="showBindModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">绑定 TOTP 密钥</h3>

        <div class="text-center mb-4">
          <p class="text-sm text-gray-600 mb-3">使用 Google Authenticator 或其他认证器扫描二维码：</p>
          <img v-if="bindData" :src="'data:image/png;base64,' + bindData.qr_base64" alt="TOTP QR Code" class="mx-auto" />
        </div>

        <div class="mb-4">
          <p class="text-xs text-gray-500 mb-1">手动输入密钥：</p>
          <code v-if="bindData" class="block bg-gray-100 px-2 py-1 text-xs rounded break-all">{{ bindData.secret }}</code>
        </div>

        <div class="flex justify-end gap-2">
          <button @click="cancelBind" class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="confirmBind" class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
            我已扫码绑定
          </button>
        </div>
      </div>
    </div>

    <!-- 启用二次验证弹窗（输入验证码） -->
    <div v-if="showEnableModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">启用二次验证</h3>
        <p class="text-sm text-gray-600 mb-4">请输入认证器上显示的 6 位验证码确认启用：</p>

        <div class="mb-4">
          <input v-model="enableCode" type="text" maxlength="6" placeholder="6位数字验证码" autofocus
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
        </div>

        <div v-if="enableError" class="text-sm text-red-600 mb-3">{{ enableError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="showEnableModal = false; enableCode = ''; enableError = ''"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleEnableTOTP" :disabled="enableLoading || !enableCode"
            class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ enableLoading ? '验证中...' : '确认启用' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 禁用二次验证弹窗 -->
    <div v-if="showDisableModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">禁用二次验证</h3>
        <p class="text-sm text-gray-600 mb-4">请输入密码或当前验证码确认禁用（TOTP 密钥会保留）：</p>

        <div class="space-y-3 mb-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
            <input v-model="disablePassword" type="password" placeholder="输入密码"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div class="text-center text-gray-400 text-sm">或</div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">验证码</label>
            <input v-model="disableCode" type="text" maxlength="6" placeholder="6位数字验证码"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
        </div>

        <div v-if="disableError" class="text-sm text-red-600 mb-3">{{ disableError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="showDisableModal = false; disablePassword = ''; disableCode = ''; disableError = ''"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleDisableTOTP" :disabled="disableLoading || (!disablePassword && !disableCode)"
            class="px-4 py-2 bg-red-600 text-white text-sm rounded-md hover:bg-red-700 disabled:opacity-50">
            {{ disableLoading ? '验证中...' : '确认禁用' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 重置 TOTP 密钥弹窗 -->
    <div v-if="showResetModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">重置 TOTP 密钥</h3>
        <p class="text-sm text-gray-600 mb-4">
          重置后需重新扫码绑定认证器。若二次验证或临时登录已启用，重置会导致它们失效（需重新启用）。
          请输入密码或当前验证码确认：
        </p>

        <div class="space-y-3 mb-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
            <input v-model="resetPassword" type="password" placeholder="输入密码"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div class="text-center text-gray-400 text-sm">或</div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">验证码</label>
            <input v-model="resetCode" type="text" maxlength="6" placeholder="6位数字验证码"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
        </div>

        <div v-if="resetError" class="text-sm text-red-600 mb-3">{{ resetError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="showResetModal = false; resetPassword = ''; resetCode = ''; resetError = ''"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleResetTOTP" :disabled="resetLoading || (!resetPassword && !resetCode)"
            class="px-4 py-2 bg-amber-600 text-white text-sm rounded-md hover:bg-amber-700 disabled:opacity-50">
            {{ resetLoading ? '验证中...' : '确认重置' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 启用临时登录弹窗（设置 PIN） -->
    <div v-if="showTempSetupModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">启用临时登录</h3>
        <p class="text-sm text-gray-600 mb-4">设置一个 6 位数字 PIN，配合 TOTP 验证码使用：</p>

        <div class="space-y-3 mb-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">PIN（6 位数字）</label>
            <input v-model="tempNewPIN" type="text" inputmode="numeric" maxlength="6" placeholder="6位数字" autofocus
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">确认 PIN</label>
            <input v-model="tempNewPINConfirm" type="text" inputmode="numeric" maxlength="6" placeholder="再次输入"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>
        </div>

        <div v-if="tempSetupError" class="text-sm text-red-600 mb-3">{{ tempSetupError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="showTempSetupModal = false; tempNewPIN = ''; tempNewPINConfirm = ''; tempSetupError = ''"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleConfirmEnableTempLogin" :disabled="tempSetupLoading"
            class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ tempSetupLoading ? '处理中...' : '确认启用' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 修改临时登录 PIN 弹窗 -->
    <div v-if="showTempPINModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">修改临时登录 PIN</h3>

        <div class="space-y-3 mb-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">新 PIN（6 位数字）</label>
            <input v-model="tempNewPIN" type="text" inputmode="numeric" maxlength="6" placeholder="6位数字" autofocus
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">确认 PIN</label>
            <input v-model="tempNewPINConfirm" type="text" inputmode="numeric" maxlength="6" placeholder="再次输入"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>
        </div>

        <div v-if="tempPINError" class="text-sm text-red-600 mb-3">{{ tempPINError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="showTempPINModal = false; tempNewPIN = ''; tempNewPINConfirm = ''; tempPINError = ''"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleUpdateTempPIN" :disabled="tempPINLoading"
            class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ tempPINLoading ? '保存中...' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 注册通行密钥弹窗（TOTP 再验证 + 设备名） -->
    <div v-if="showRegisterModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">注册通行密钥</h3>

        <div class="space-y-3 mb-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">TOTP 验证码</label>
            <input v-model="registerCode" type="text" inputmode="numeric" maxlength="6" placeholder="6位数字" autofocus
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">设备名（便于识别）</label>
            <input v-model="registerDeviceName" type="text" maxlength="64" placeholder="如：我的手机"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
        </div>

        <div v-if="registerError" class="text-sm text-red-600 mb-3">{{ registerError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="cancelRegister"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleRegisterPasskey" :disabled="registerLoading"
            class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ registerLoading ? '注册中...' : '开始注册' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 重命名通行密钥弹窗 -->
    <div v-if="showRenameModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">重命名通行密钥</h3>

        <div class="mb-4">
          <label class="block text-sm font-medium text-gray-700 mb-1">设备名</label>
          <input v-model="renameDeviceName" type="text" maxlength="64" autofocus
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
        </div>

        <div v-if="renameError" class="text-sm text-red-600 mb-3">{{ renameError }}</div>

        <div class="flex justify-end gap-2">
          <button @click="showRenameModal = false; renameError = ''"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleRenameCredential" :disabled="renameLoading"
            class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ renameLoading ? '保存中...' : '保存' }}
          </button>
        </div>
      </div>
    </div>

    <!-- 重置应用 API token 弹窗 -->
    <div v-if="showAppApiResetModal" class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 p-4">
      <div class="bg-white rounded-lg shadow-xl max-w-sm w-full p-6">
        <h3 class="text-lg font-medium mb-4">重置应用 API Token</h3>
        <p class="text-sm text-gray-600 mb-4">
          重置后旧 token 立即失效，所有使用旧 token 的调用会被拒绝。新 token 仅在此处显示一次。
        </p>
        <div class="flex justify-end gap-2">
          <button @click="showAppApiResetModal = false"
            class="px-4 py-2 text-sm text-gray-600 hover:text-gray-800">取消</button>
          <button @click="handleResetAppApiToken" :disabled="appApiLoading"
            class="px-4 py-2 bg-amber-600 text-white text-sm rounded-md hover:bg-amber-700 disabled:opacity-50">
            {{ appApiLoading ? '处理中...' : '确认重置' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { changePassword } from '../api/auth'
import { getTOTPStatus, setupTOTP, enableTOTP, disableTOTP, resetTOTP, type TOTPSetup } from '../api/totp'
import { getUserSettings, updateUserSettings, type UserSettings } from '../api/settings'
import {
  listCredentials, renameCredential, deleteCredential, registerPasskey,
  type WebAuthnCredentialInfo,
} from '../api/webauthn'
import LoadingSpinner from '../components/LoadingSpinner.vue'

const pwForm = ref({ oldPassword: '', newPassword: '' })
const pwMsg = ref('')
const pwError = ref(false)

// 自动禁用天数设置
const autoDisableDays = ref(0)
const autoDisableMsg = ref('')
const autoDisableError = ref(false)
const autoDisableLoading = ref(false)

// 快速登录开关
const quickLogin = ref(false)
const quickLoginMsg = ref('')
const quickLoginError = ref(false)
const quickLoginLoading = ref(false)

// 应用 API 开关与 token
const appApiEnabled = ref(false)
const appApiTokenSet = ref(false)   // 后端是否已存 token（与 enabled 独立）
const appApiToken = ref('')        // 本地保存的完整 token，仅供本机展示
const appApiMsg = ref('')
const appApiError = ref(false)
const appApiLoading = ref(false)
const appApiCopied = ref(false)
const showAppApiResetModal = ref(false)

// TOTP 密钥状态（独立基础）
const totpSecretSet = ref(false)
const totpEnabled = ref(false) // 二次验证开关
const tempLoginEnabled = ref(false) // 临时登录开关
const settingsLoading = ref(true)

// 绑定密钥弹窗
const showBindModal = ref(false)
const bindLoading = ref(false)
const bindData = ref<TOTPSetup | null>(null)

// 启用二次验证弹窗
const showEnableModal = ref(false)
const enableCode = ref('')
const enableError = ref('')
const enableLoading = ref(false)

// 禁用二次验证弹窗
const showDisableModal = ref(false)
const disablePassword = ref('')
const disableCode = ref('')
const disableError = ref('')
const disableLoading = ref(false)

// 重置密钥弹窗
const showResetModal = ref(false)
const resetPassword = ref('')
const resetCode = ref('')
const resetError = ref('')
const resetLoading = ref(false)

// 启用临时登录弹窗
const showTempSetupModal = ref(false)
const tempSetupLoading = ref(false)
const tempSetupError = ref('')
const tempNewPIN = ref('')
const tempNewPINConfirm = ref('')

// 修改 PIN 弹窗
const showTempPINModal = ref(false)
const tempPINError = ref('')
const tempPINLoading = ref(false)

// === 通行密钥（#71） ===
const passkeySupported = ref(true)
const passkeyLoading = ref(true)
const credentials = ref<WebAuthnCredentialInfo[]>([])

// 注册弹窗
const showRegisterModal = ref(false)
const registerCode = ref('')
const registerDeviceName = ref('')
const registerError = ref('')
const registerLoading = ref(false)

// 重命名弹窗
const showRenameModal = ref(false)
const renameDeviceName = ref('')
const renameError = ref('')
const renameLoading = ref(false)
const renameTarget = ref<WebAuthnCredentialInfo | null>(null)

function formatDate(iso: string): string {
  try {
    return new Date(iso).toLocaleString('zh-CN', { hour12: false })
  } catch {
    return iso
  }
}

async function loadCredentials() {
  passkeySupported.value = typeof navigator !== 'undefined' && !!window.PublicKeyCredential
  if (!passkeySupported.value) {
    passkeyLoading.value = false
    return
  }
  const res = await listCredentials()
  if (res.data) {
    credentials.value = res.data
  }
  passkeyLoading.value = false
}

function cancelRegister() {
  showRegisterModal.value = false
  registerCode.value = ''
  registerDeviceName.value = ''
  registerError.value = ''
}

async function handleRegisterPasskey() {
  registerError.value = ''
  if (registerCode.value.length !== 6) {
    registerError.value = '请输入 6 位验证码'
    return
  }
  registerLoading.value = true
  try {
    const err = await registerPasskey(registerCode.value, registerDeviceName.value.trim())
    if (err) {
      registerError.value = err
      return
    }
    cancelRegister()
    await loadCredentials()
  } catch {
    registerError.value = '注册失败，请重试'
  } finally {
    registerLoading.value = false
  }
}

function openRenameModal(cred: WebAuthnCredentialInfo) {
  renameTarget.value = cred
  renameDeviceName.value = cred.device_name
  renameError.value = ''
  showRenameModal.value = true
}

async function handleRenameCredential() {
  if (!renameTarget.value) return
  renameError.value = ''
  const name = renameDeviceName.value.trim()
  if (!name) {
    renameError.value = '设备名不能为空'
    return
  }
  renameLoading.value = true
  const res = await renameCredential(renameTarget.value.id, name)
  renameLoading.value = false
  if (res.error) {
    renameError.value = res.error
    return
  }
  showRenameModal.value = false
  await loadCredentials()
}

async function handleDeleteCredential(cred: WebAuthnCredentialInfo) {
  if (!confirm(`确定删除通行密钥「${cred.device_name || '未命名设备'}」？删除后该设备将无法用通行密钥登录。`)) {
    return
  }
  const res = await deleteCredential(cred.id)
  if (res.error) {
    alert(res.error)
    return
  }
  await loadCredentials()
}

async function handleChangePassword() {
  pwMsg.value = ''
  const res = await changePassword(pwForm.value.oldPassword, pwForm.value.newPassword)
  if (res.error) {
    pwMsg.value = res.error
    pwError.value = true
  } else {
    pwMsg.value = '密码修改成功'
    pwError.value = false
    pwForm.value = { oldPassword: '', newPassword: '' }
  }
}

async function handleUpdateAutoDisable() {
  autoDisableMsg.value = ''
  autoDisableLoading.value = true
  const res = await updateUserSettings({ auto_disable_days: autoDisableDays.value })
  autoDisableLoading.value = false
  if (res.error) {
    autoDisableMsg.value = res.error
    autoDisableError.value = true
  } else {
    autoDisableMsg.value = '设置已保存'
    autoDisableError.value = false
  }
}

async function handleUpdateQuickLogin() {
  quickLoginMsg.value = ''
  quickLoginLoading.value = true
  const res = await updateUserSettings({ quick_login: quickLogin.value })
  quickLoginLoading.value = false
  if (res.error) {
    quickLoginMsg.value = res.error
    quickLoginError.value = true
    // 失败时回滚开关到上次已知状态
    const verifyRes = await getUserSettings()
    if (verifyRes.data) {
      quickLogin.value = verifyRes.data.quick_login
    }
  } else {
    quickLoginMsg.value = '设置已保存'
    quickLoginError.value = false
  }
}

// === 应用 API ===

function clearAppApiMsg() {
  appApiMsg.value = ''
  appApiError.value = false
}

async function handleToggleAppApi() {
  clearAppApiMsg()
  appApiLoading.value = true
  const res = await updateUserSettings({ app_api_enabled: appApiEnabled.value })
  appApiLoading.value = false
  if (res.error) {
    appApiMsg.value = res.error
    appApiError.value = true
    // 失败回滚
    const verifyRes = await getUserSettings()
    if (verifyRes.data) {
      appApiEnabled.value = verifyRes.data.app_api_enabled
      appApiTokenSet.value = verifyRes.data.app_api_token_set
    }
    return
  }
  // 启用并首次生成 token 时后端返回完整 token
  if (appApiEnabled.value && res.data?.app_api_token) {
    appApiToken.value = res.data.app_api_token
    appApiTokenSet.value = true
    appApiMsg.value = '已启用，请妥善保存 token'
  } else {
    appApiMsg.value = '设置已保存'
    // 关闭后清空本地 token 展示
    if (!appApiEnabled.value) {
      appApiToken.value = ''
      appApiTokenSet.value = false
    }
  }
}

async function handleResetAppApiToken() {
  clearAppApiMsg()
  appApiLoading.value = true
  const res = await updateUserSettings({ reset_app_api_token: true })
  appApiLoading.value = false
  if (res.error) {
    appApiMsg.value = res.error
    appApiError.value = true
    return
  }
  if (res.data?.app_api_token) {
    appApiToken.value = res.data.app_api_token
    appApiTokenSet.value = true
    appApiEnabled.value = true
    appApiMsg.value = 'token 已重置，请妥善保存新 token'
  }
  showAppApiResetModal.value = false
}

async function copyAppApiToken() {
  try {
    await navigator.clipboard.writeText(appApiToken.value)
    appApiCopied.value = true
    setTimeout(() => { appApiCopied.value = false }, 2000)
  } catch {
    // ignore
  }
}

async function loadSettings() {
  const [totpRes, settingsRes] = await Promise.all([
    getTOTPStatus(),
    getUserSettings(),
  ])
  if (totpRes.data) {
    totpEnabled.value = totpRes.data.totp_enabled
  }
  if (settingsRes.data) {
    const data = settingsRes.data as UserSettings
    autoDisableDays.value = data.auto_disable_days
    tempLoginEnabled.value = data.temp_login_enabled
    totpSecretSet.value = data.totp_secret_set
    quickLogin.value = data.quick_login
    appApiEnabled.value = data.app_api_enabled
    appApiTokenSet.value = data.app_api_token_set
    appApiToken.value = data.app_api_token || ''
  }
  settingsLoading.value = false
  loadCredentials()
}

// === TOTP 密钥管理 ===

async function handleBindTOTP() {
  bindLoading.value = true
  const res = await setupTOTP()
  bindLoading.value = false
  if (res.error) {
    pwMsg.value = res.error
    pwError.value = true
  } else {
    bindData.value = res.data || null
    showBindModal.value = true
  }
}

function cancelBind() {
  showBindModal.value = false
  bindData.value = null
}

function confirmBind() {
  // 密钥已由 setupTOTP 端点保存，这里只是用户确认已扫码
  showBindModal.value = false
  totpSecretSet.value = true
  bindData.value = null
}

async function handleResetTOTP() {
  resetError.value = ''

  const data: { password?: string; code?: string } = {}
  if (resetPassword.value) {
    data.password = resetPassword.value
  } else if (resetCode.value) {
    data.code = resetCode.value
  } else {
    resetError.value = '请输入密码或验证码'
    return
  }

  resetLoading.value = true
  const res = await resetTOTP(data)
  resetLoading.value = false

  if (res.error) {
    resetError.value = res.error
  } else {
    showResetModal.value = false
    // 重置后密钥清空，依赖密钥的功能都失效
    totpSecretSet.value = false
    totpEnabled.value = false
    tempLoginEnabled.value = false
    resetPassword.value = ''
    resetCode.value = ''
  }
}

// === 二次验证 ===

async function handleEnableTOTP() {
  enableError.value = ''
  if (!enableCode.value) {
    enableError.value = '请输入验证码'
    return
  }

  enableLoading.value = true
  const res = await enableTOTP(enableCode.value)
  enableLoading.value = false

  if (res.error) {
    enableError.value = res.error
  } else {
    showEnableModal.value = false
    totpEnabled.value = true
    enableCode.value = ''
  }
}

async function handleDisableTOTP() {
  disableError.value = ''

  const data: { password?: string; code?: string } = {}
  if (disablePassword.value) {
    data.password = disablePassword.value
  } else if (disableCode.value) {
    data.code = disableCode.value
  } else {
    disableError.value = '请输入密码或验证码'
    return
  }

  disableLoading.value = true
  const res = await disableTOTP(data)
  disableLoading.value = false

  if (res.error) {
    disableError.value = res.error
  } else {
    showDisableModal.value = false
    totpEnabled.value = false
    // TOTP 密钥保留，重置走独立功能
    disablePassword.value = ''
    disableCode.value = ''
  }
}

// === 临时登录 ===

async function handleConfirmEnableTempLogin() {
  tempSetupError.value = ''
  if (tempNewPIN.value.length !== 6 || !/^\d{6}$/.test(tempNewPIN.value)) {
    tempSetupError.value = 'PIN 必须为 6 位数字'
    return
  }
  if (tempNewPIN.value !== tempNewPINConfirm.value) {
    tempSetupError.value = '两次输入的 PIN 不一致'
    return
  }

  tempSetupLoading.value = true
  const res = await updateUserSettings({ temp_login_enabled: true, temp_login_pin: tempNewPIN.value })
  tempSetupLoading.value = false

  if (res.error) {
    tempSetupError.value = res.error
  } else {
    // 显式验证后端真的写入了
    const verifyRes = await getUserSettings()
    if (verifyRes.data?.temp_login_enabled) {
      tempLoginEnabled.value = true
      showTempSetupModal.value = false
      tempNewPIN.value = ''
      tempNewPINConfirm.value = ''
    } else {
      tempSetupError.value = '启用失败，请重试'
    }
  }
}

async function handleUpdateTempPIN() {
  tempPINError.value = ''
  if (tempNewPIN.value.length !== 6 || !/^\d{6}$/.test(tempNewPIN.value)) {
    tempPINError.value = 'PIN 必须为 6 位数字'
    return
  }
  if (tempNewPIN.value !== tempNewPINConfirm.value) {
    tempPINError.value = '两次输入的 PIN 不一致'
    return
  }

  tempPINLoading.value = true
  const res = await updateUserSettings({ temp_login_pin: tempNewPIN.value })
  tempPINLoading.value = false

  if (res.error) {
    tempPINError.value = res.error
  } else {
    showTempPINModal.value = false
    tempNewPIN.value = ''
    tempNewPINConfirm.value = ''
  }
}

async function handleDisableTempLogin() {
  if (!confirm('确定关闭临时登录？')) return
  const res = await updateUserSettings({ temp_login_enabled: false })
  if (res.error) {
    pwMsg.value = res.error
    pwError.value = true
  } else {
    tempLoginEnabled.value = false
    // TOTP 密钥保留
  }
}

onMounted(() => {
  loadSettings()
})
</script>
