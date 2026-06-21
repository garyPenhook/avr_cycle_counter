; r24 = r24 * 10 = (r24<<3) + (r24<<1), shift-and-add, no multiplier, no R1 fixup.
; Compare against the multiply version:
;   cyclecount -vs examples/scale_mul.S examples/scale_shift.S

        .section .text
        .global scale
scale:
        mov     r25, r24
        lsl     r25             ; r25 = x2
        lsl     r24
        lsl     r24
        lsl     r24             ; r24 = x8
        add     r24, r25        ; x8 + x2 = x10
        ret
