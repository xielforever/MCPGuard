<template>
  <div class="settings">
    <el-card shadow="never">
      <el-tabs v-model="activeTab">
        <el-tab-pane label="General" name="general">
          <el-form :model="generalSettings" label-width="150px" style="max-width: 600px">
            <el-form-item label="Code Length">
              <el-input-number v-model="generalSettings.codeLength" :min="4" :max="8" />
            </el-form-item>
            <el-form-item label="Code Expiry (mins)">
              <el-input-number v-model="generalSettings.codeExpiry" :min="1" />
            </el-form-item>
            <el-form-item label="Max Attempts">
              <el-input-number v-model="generalSettings.maxAttempts" :min="1" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary">Save Changes</el-button>
            </el-form-item>
          </el-form>
        </el-tab-pane>

        <el-tab-pane label="Notifications" name="notifications">
          <el-form :model="notifSettings" label-width="150px" style="max-width: 600px">
            <el-form-item label="Provider">
              <el-select v-model="notifSettings.provider" style="width: 100%">
                <el-option label="Telegram" value="telegram" />
                <el-option label="Slack" value="slack" />
                <el-option label="Webhook" value="webhook" />
              </el-select>
            </el-form-item>
            <el-form-item label="API Key" v-if="notifSettings.provider !== 'webhook'">
              <el-input v-model="notifSettings.apiKey" type="password" show-password />
            </el-form-item>
            <el-form-item label="Webhook URL" v-if="notifSettings.provider === 'webhook'">
              <el-input v-model="notifSettings.webhookUrl" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary">Save Changes</el-button>
              <el-button>Test Notification</el-button>
            </el-form-item>
          </el-form>
        </el-tab-pane>

        <el-tab-pane label="Proxy" name="proxy">
          <el-form :model="proxySettings" label-width="150px" style="max-width: 600px">
            <el-form-item label="Backend Command">
              <el-input v-model="proxySettings.backendCommand" />
            </el-form-item>
            <el-form-item label="Backend Args">
              <el-input v-model="proxySettings.backendArgs" placeholder="Comma separated" />
            </el-form-item>
            <el-form-item label="Transport Mode">
              <el-select v-model="proxySettings.transportMode" style="width: 100%">
                <el-option label="Stdio" value="stdio" />
                <el-option label="SSE" value="sse" />
              </el-select>
            </el-form-item>
            <el-form-item>
              <el-button type="primary">Save Changes</el-button>
            </el-form-item>
          </el-form>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>

<script setup>
import { ref } from 'vue'

const activeTab = ref('general')

const generalSettings = ref({
  codeLength: 6,
  codeExpiry: 5,
  maxAttempts: 3
})

const notifSettings = ref({
  provider: 'telegram',
  apiKey: '',
  webhookUrl: ''
})

const proxySettings = ref({
  backendCommand: 'node',
  backendArgs: 'server.js',
  transportMode: 'stdio'
})
</script>
