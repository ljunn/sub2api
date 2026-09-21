import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, nextTick, ref } from 'vue'
import BaseDialog from '../BaseDialog.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('BaseDialog', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.body.classList.remove('modal-open')
  })

  it('resets body scroll position when reopened', async () => {
    const wrapper = mount(BaseDialog, {
      attachTo: document.body,
      props: { show: false, title: 'Details' },
      slots: { default: '<div style="height: 2000px">content</div>' },
      global: { stubs: { Icon: true } }
    })

    await wrapper.setProps({ show: true })
    await nextTick()
    const body = document.body.querySelector<HTMLElement>('.modal-body')
    expect(body).not.toBeNull()
    body!.scrollTop = 480

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await nextTick()

    expect(document.body.querySelector<HTMLElement>('.modal-body')?.scrollTop).toBe(0)
    wrapper.unmount()
  })

  it('keeps the page locked while replacing details with an editor and back', async () => {
    const editing = ref(false)
    const wrapper = mount(defineComponent({
      components: { BaseDialog },
      setup: () => ({ editing }),
      template: '<BaseDialog v-if="!editing" key="details" :show="true" title="Details" /><BaseDialog v-else key="edit" :show="true" title="Edit" />',
    }), { attachTo: document.body, global: { stubs: { Icon: true } } })
    await nextTick()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    editing.value = true
    await nextTick()
    expect(document.body.querySelectorAll('[role="dialog"]')).toHaveLength(1)
    expect(document.body.classList.contains('modal-open')).toBe(true)
    editing.value = false
    await nextTick()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    wrapper.unmount()
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })

  it('does not let a hidden or closing sibling unlock an open dialog', async () => {
    const first = mount(BaseDialog, { props: { show: true, title: 'First' }, global: { stubs: { Icon: true } } })
    const second = mount(BaseDialog, { props: { show: false, title: 'Second' }, global: { stubs: { Icon: true } } })
    expect(document.body.classList.contains('modal-open')).toBe(true)
    await second.setProps({ show: true })
    await first.setProps({ show: false })
    expect(document.body.classList.contains('modal-open')).toBe(true)
    first.unmount()
    expect(document.body.classList.contains('modal-open')).toBe(true)
    second.unmount()
    expect(document.body.classList.contains('modal-open')).toBe(false)
  })
})
