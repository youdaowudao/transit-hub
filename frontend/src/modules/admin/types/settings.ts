export type NotificationChannel = 'telegram'
export type NotificationTemplateFormat = 'text' | 'markdown' | 'html'

export type TestNotificationChannelPayload = {
  channel: NotificationChannel
  telegramBotToken?: string
  telegramChatId?: string
  telegramProxyUrl?: string
}

export type TestNotificationChannelResponse = {
  success: boolean
  message: string
}

export type NotificationChannelSettings = {
  telegram: TelegramChannelSettings[]
}

export type StrategySettings = {
  enableRefreshInterval: boolean
  refreshInterval: number
  enableBalanceWarning: boolean
  defaultBalanceThreshold: number
  balanceNotifyBotIds: string[]
  balanceNotifyRecipientsInvalid?: boolean
  balanceTemplate: string
  balanceTemplateFormat?: NotificationTemplateFormat
  enableMultiplierAlert: boolean
  multiplierNotifyBotIds: string[]
  multiplierNotifyRecipientsInvalid?: boolean
  multiplierTemplate: string
  multiplierTemplateFormat?: NotificationTemplateFormat
}

export type TelegramChannelSettings = {
  id: string
  name: string
  enabled: boolean
  botToken: string
  chatId: string
  proxyUrl: string
}

export type SmtpTlsMode = 'implicit' | 'starttls'

export type SmtpSettings = {
  host: string
  port: number
  username: string
  fromEmail: string
  fromName: string
  tlsMode: SmtpTlsMode
  passwordConfigured: boolean
  updatedAt: string | null
}

export type SaveSmtpSettingsPayload = {
  host: string
  port: number
  username: string
  password?: string
  fromEmail: string
  fromName: string
  tlsMode: SmtpTlsMode
}

export type TestSmtpEmailPayload = {
  recipientEmail: string
}

export type TestSmtpEmailResponse = {
  success: boolean
  message: string
}

export type EmailTemplate = {
  id: string
  name: string
  subject: string
  htmlBody: string
  isBuiltIn: boolean
  createdAt: string | null
  updatedAt: string | null
}

export type SaveEmailTemplatePayload = {
  name: string
  subject: string
  htmlBody: string
}

export type TestEmailTemplatePayload = {
  recipientEmail: string
}

export type TestEmailTemplateResponse = {
  success: boolean
  message: string
}
