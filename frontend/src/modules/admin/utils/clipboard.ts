export const copyText = async (text: string): Promise<void> => {
  if (typeof navigator.clipboard?.writeText === 'function') {
    await navigator.clipboard.writeText(text)
    return
  }

  const originalFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.readOnly = true
  textarea.tabIndex = -1
  textarea.setAttribute('aria-hidden', 'true')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  try {
    document.body.appendChild(textarea)
    textarea.focus()
    textarea.select()
    textarea.setSelectionRange(0, text.length)
    if (typeof document.execCommand !== 'function' || document.execCommand('copy') !== true) {
      throw new Error('浏览器未完成复制；请手动选择内容后复制。')
    }
  } finally {
    textarea.remove()
    if (originalFocus?.isConnected) originalFocus.focus({ preventScroll: true })
  }
}
