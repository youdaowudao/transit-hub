<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Check, CircleOff, Edit3, Loader2, Plus, Save, Star, Trash2, X } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { t } from '@/locales'
import {
  createTestQuestion,
  deleteTestQuestion,
  listTestQuestions,
  setDefaultTestQuestion,
  setTestQuestionEnabled,
  updateTestQuestion,
} from '../../api/connectionHealth'
import type { TestQuestion } from '../../types/connectionHealth'
import {
  TEST_QUESTION_KEYWORD_BYTES_LIMIT,
  TEST_QUESTION_KEYWORD_COUNT_LIMIT,
  TEST_QUESTION_KEYWORD_RUNE_LIMIT,
  parseTestQuestionKeywords,
  testQuestionKeywordBytes,
} from '../../utils/questionAnswers'

const questions = ref<TestQuestion[]>([])
const loading = ref(true)
const saving = ref(false)
const actionId = ref('')
const errorKey = ref('')
const editingId = ref('')
const name = ref('')
const body = ref('')
const keywordInput = ref('')

const nameLength = computed(() => Array.from(name.value).length)
const bodyLength = computed(() => Array.from(body.value).length)
const parsedKeywords = computed(() => parseTestQuestionKeywords(keywordInput.value))
const keywordBytes = computed(() => testQuestionKeywordBytes(parsedKeywords.value))
const keywordValidationErrorKey = computed(() => {
  if (parsedKeywords.value.some(keyword => Array.from(keyword).length > TEST_QUESTION_KEYWORD_RUNE_LIMIT)) {
    return 'admin.connectionHealth.errors.testQuestionKeywordLength'
  }
  if (parsedKeywords.value.length > TEST_QUESTION_KEYWORD_COUNT_LIMIT) {
    return 'admin.connectionHealth.errors.testQuestionKeywordCount'
  }
  if (keywordBytes.value > TEST_QUESTION_KEYWORD_BYTES_LIMIT) {
    return 'admin.connectionHealth.errors.testQuestionKeywordBytes'
  }
  return ''
})
const showValidationError = computed(() => (
  (name.value.length > 0 || body.value.length > 0)
  && (name.value.trim().length === 0 || body.value.trim().length === 0 || nameLength.value > 100 || bodyLength.value > 4000)
))
const canSave = computed(() => (
  name.value.trim().length > 0
  && body.value.trim().length > 0
  && nameLength.value <= 100
  && bodyLength.value <= 4000
  && !keywordValidationErrorKey.value
  && !saving.value
))
const errorText = computed(() => errorKey.value.startsWith('admin.') ? t(errorKey.value) : errorKey.value)

const load = async () => {
  loading.value = true
  errorKey.value = ''
  try {
    questions.value = await listTestQuestions()
  } catch (error) {
    errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    loading.value = false
  }
}

const resetForm = () => {
  editingId.value = ''
  name.value = ''
  body.value = ''
  keywordInput.value = ''
}

const edit = (question: TestQuestion) => {
  editingId.value = question.id
  name.value = question.name
  body.value = question.body
  keywordInput.value = question.keywords.join('\n')
  errorKey.value = ''
}

const save = async () => {
  if (!canSave.value) return
  saving.value = true
  errorKey.value = ''
  try {
    const input = {
      name: name.value.trim(),
      body: body.value.trim(),
      keywords: [...parsedKeywords.value],
    }
    if (editingId.value) {
      await updateTestQuestion(editingId.value, input)
    } else {
      await createTestQuestion(input)
    }
    resetForm()
    await load()
  } catch (error) {
    errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    saving.value = false
  }
}

const toggleEnabled = async (question: TestQuestion) => {
  actionId.value = question.id
  errorKey.value = ''
  try {
    await setTestQuestionEnabled(question.id, !question.enabled)
    await load()
  } catch (error) {
    errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    actionId.value = ''
  }
}

const makeDefault = async (question: TestQuestion) => {
  if (!question.enabled || question.isDefault) return
  actionId.value = question.id
  errorKey.value = ''
  try {
    await setDefaultTestQuestion(question.id)
    await load()
  } catch (error) {
    errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    actionId.value = ''
  }
}

const remove = async (question: TestQuestion) => {
  if (!window.confirm(t('admin.settings.testQuestions.deleteConfirm', { name: question.name }))) return
  actionId.value = question.id
  errorKey.value = ''
  try {
    await deleteTestQuestion(question.id)
    if (editingId.value === question.id) resetForm()
    await load()
  } catch (error) {
    errorKey.value = error instanceof Error ? error.message : 'admin.connectionHealth.errors.request'
  } finally {
    actionId.value = ''
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-lg font-semibold text-foreground">{{ t('admin.settings.testQuestions.title') }}</h3>
      <p class="mt-0.5 text-sm text-muted-foreground">{{ t('admin.settings.testQuestions.description') }}</p>
    </div>

    <p v-if="errorKey" class="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">
      {{ errorText }}
    </p>

    <section class="w-full overflow-hidden rounded-xl border border-border/50 bg-card shadow-sm">
      <div class="space-y-4 p-5">
        <div class="flex items-center justify-between gap-3">
          <h4 class="text-sm font-semibold text-foreground">
            {{ editingId ? t('admin.settings.testQuestions.editTitle') : t('admin.settings.testQuestions.createTitle') }}
          </h4>
          <button
            v-if="editingId"
            type="button"
            class="rounded-md p-1.5 text-muted-foreground hover:bg-surface-elevated hover:text-foreground"
            :title="t('admin.settings.testQuestions.cancelEdit')"
            @click="resetForm"
          >
            <X class="h-4 w-4" />
          </button>
        </div>
         <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <label for="test-question-name" class="text-xs font-medium text-muted-foreground">{{ t('admin.settings.testQuestions.name') }}</label>
            <span class="text-xs text-muted-foreground">{{ nameLength }}/100</span>
          </div>
          <Input id="test-question-name" v-model="name" maxlength="100" :placeholder="t('admin.settings.testQuestions.namePlaceholder')" />
        </div>
        <div class="grid gap-2">
          <div class="flex items-center justify-between gap-3">
            <label for="test-question-body" class="text-xs font-medium text-muted-foreground">{{ t('admin.settings.testQuestions.body') }}</label>
            <span class="text-xs text-muted-foreground">{{ bodyLength }}/4000</span>
          </div>
          <textarea
            id="test-question-body"
            v-model="body"
            maxlength="4000"
            rows="7"
            class="w-full resize-y rounded-lg border border-input bg-background px-3 py-2 text-sm text-foreground outline-none transition-colors placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-primary"
            :placeholder="t('admin.settings.testQuestions.bodyPlaceholder')"
           />
         </div>
        <div class="grid gap-2">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <label for="test-question-keywords" class="text-xs font-medium text-muted-foreground">{{ t('admin.settings.testQuestions.keywords') }}</label>
            <div class="flex items-center gap-3 text-xs text-muted-foreground">
              <span data-testid="test-question-keyword-count">{{ parsedKeywords.length }}/{{ TEST_QUESTION_KEYWORD_COUNT_LIMIT }}</span>
              <span data-testid="test-question-keyword-bytes">{{ keywordBytes }}/{{ TEST_QUESTION_KEYWORD_BYTES_LIMIT }}</span>
            </div>
          </div>
          <textarea
            id="test-question-keywords"
            v-model="keywordInput"
            rows="3"
            class="w-full resize-y rounded-lg border border-input bg-background px-3 py-2 text-sm text-foreground outline-none transition-colors placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-primary"
            :placeholder="t('admin.settings.testQuestions.keywordsPlaceholder')"
          />
          <p class="text-xs text-muted-foreground">{{ t('admin.settings.testQuestions.keywordsHint') }}</p>
        </div>
        <p v-if="showValidationError" class="text-xs text-destructive">
          {{ t('admin.connectionHealth.errors.testQuestionInvalid') }}
        </p>
        <p v-if="keywordValidationErrorKey" class="text-xs text-destructive">
          {{ t(keywordValidationErrorKey) }}
        </p>
         <div class="flex justify-end">
          <Button :disabled="!canSave" @click="save">
            <Loader2 v-if="saving" class="mr-2 h-4 w-4 animate-spin" />
            <Save v-else-if="editingId" class="mr-2 h-4 w-4" />
            <Plus v-else class="mr-2 h-4 w-4" />
            {{ editingId ? t('admin.settings.testQuestions.saveEdit') : t('admin.settings.testQuestions.add') }}
          </Button>
        </div>
      </div>
    </section>

    <section class="w-full overflow-hidden rounded-xl border border-border/50 bg-card shadow-sm">
      <div class="border-b border-border/40 px-5 py-4">
        <h4 class="text-sm font-semibold text-foreground">{{ t('admin.settings.testQuestions.listTitle') }}</h4>
      </div>
      <div v-if="loading" class="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
        <Loader2 class="h-5 w-5 animate-spin" />
        {{ t('admin.settings.testQuestions.loading') }}
      </div>
      <div v-else-if="questions.length === 0" class="py-12 text-center text-sm text-muted-foreground">
        {{ t('admin.settings.testQuestions.empty') }}
      </div>
      <ul v-else class="divide-y divide-border/40">
        <li v-for="question in questions" :key="question.id" class="flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-start sm:justify-between">
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <h5 class="text-sm font-medium text-foreground">{{ question.name }}</h5>
              <span v-if="question.isDefault" class="inline-flex items-center gap-1 rounded-full bg-amber-500/10 px-2 py-0.5 text-xs text-amber-600 dark:text-amber-400">
                <Star class="h-3 w-3 fill-current" />
                {{ t('admin.settings.testQuestions.default') }}
              </span>
              <span class="rounded-full px-2 py-0.5 text-xs" :class="question.enabled ? 'bg-green-500/10 text-green-600 dark:text-green-400' : 'bg-zinc-500/10 text-zinc-500'">
                {{ question.enabled ? t('admin.settings.testQuestions.enabled') : t('admin.settings.testQuestions.disabled') }}
              </span>
            </div>
            <p class="mt-2 line-clamp-3 whitespace-pre-wrap text-xs leading-5 text-muted-foreground">{{ question.body }}</p>
            <p class="mt-2 text-xs leading-5 text-muted-foreground">
              <span class="font-medium text-foreground">{{ t('admin.settings.testQuestions.keywords') }}：</span>
              {{ question.keywords.length > 0 ? question.keywords.join('、') : t('admin.settings.testQuestions.noKeywords') }}
            </p>
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <button
              type="button"
              class="rounded-md p-2 text-muted-foreground hover:bg-surface-elevated hover:text-foreground disabled:opacity-40"
              :disabled="actionId === question.id || !question.enabled || question.isDefault"
              :title="t('admin.settings.testQuestions.setDefault')"
              @click="makeDefault(question)"
            >
              <Star class="h-4 w-4" />
            </button>
            <button
              type="button"
              class="rounded-md p-2 text-muted-foreground hover:bg-surface-elevated hover:text-foreground disabled:opacity-40"
              :disabled="actionId === question.id"
              :title="question.enabled ? t('admin.settings.testQuestions.disable') : t('admin.settings.testQuestions.enable')"
              @click="toggleEnabled(question)"
            >
              <Loader2 v-if="actionId === question.id" class="h-4 w-4 animate-spin" />
              <CircleOff v-else-if="question.enabled" class="h-4 w-4" />
              <Check v-else class="h-4 w-4" />
            </button>
            <button type="button" class="rounded-md p-2 text-muted-foreground hover:bg-surface-elevated hover:text-foreground" :title="t('admin.settings.testQuestions.edit')" @click="edit(question)">
              <Edit3 class="h-4 w-4" />
            </button>
            <button
              type="button"
              class="rounded-md p-2 text-muted-foreground hover:bg-red-500/10 hover:text-red-500 disabled:opacity-40"
              :disabled="actionId === question.id"
              :title="t('admin.settings.testQuestions.delete')"
              @click="remove(question)"
            >
              <Trash2 class="h-4 w-4" />
            </button>
          </div>
        </li>
      </ul>
    </section>
  </div>
</template>
