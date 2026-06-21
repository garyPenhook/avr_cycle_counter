; ATtiny3217 (AVRxt) — software delay + pin toggle.
; Demonstrates hot-path cycle counting with cyclecount.
;
;   cyclecount examples/delay.S
;   cyclecount -v examples/delay.S
;   cyclecount -from delay_ticks -to toggle_pin examples/delay.S

        .section .text

        .global delay_ticks
; delay_ticks(r25:r24): busy-wait r24 down to zero.
delay_ticks:
        ; @begin inner iter=1000
.L_inner:
        sbiw    r24, 1          ; 2  — decrement 16-bit counter
        brne    .L_inner        ; 2 taken / 1 on exit
        ; @end inner
.L_inner_end:
        ret

        .global toggle_pin
toggle_pin:
        push    r24
        ldi     r24, 0x20
        sts     0x0406, r24     ; write VPORTA.OUT (STS: 2 words)
        pop     r24
        ret

        .section .bss
scratch:
        .space  32              ; 32 bytes of SRAM scratch
