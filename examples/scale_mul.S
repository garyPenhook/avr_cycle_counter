; r24 = r24 * 10, using the AVRxt hardware multiplier.
; Baseline for:  cyclecount -vs examples/scale_mul.S examples/scale_shift.S

        .section .text
        .global scale
scale:
        ldi     r25, 10
        mul     r24, r25        ; R1:R0 = r24 * 10
        mov     r24, r0
        clr     r1              ; restore the ABI "zero register"
        ret
