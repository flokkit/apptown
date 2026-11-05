import { expect, it } from 'vitest'
import { pastedPictures } from './pictures'

it('shows named file downloads and byte sizes while keeping picture previews', () => {
  expect(pastedPictures('Please read these. sam attached report.csv (42 bytes): /console/uploads/report.csv sam attached cat.png (80 bytes): /console/uploads/cat.png (picture) (I pasted a picture — look at /console/uploads/cat.png)')).toEqual({
    display: 'Please read these.', names: ['cat.png'], files: [{ name: 'report.csv', size: 42 }],
  })
})

it('keeps pasted pictures in existing chat history visible', () => {
  expect(pastedPictures('Look (I pasted a picture — look at /console/uploads/picture-2.png)')).toEqual({ display: 'Look', names: ['picture-2.png'], files: [] })
})

it('does not turn a non-picture with a picture name into an inline preview', () => {
  expect(pastedPictures('sam attached picture-1.png (42 bytes): /console/uploads/picture-1.png')).toEqual({ display: '', names: [], files: [{ name: 'picture-1.png', size: 42 }] })
})
