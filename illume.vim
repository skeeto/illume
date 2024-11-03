function! IllumeAppend(channel, msg)
    let buf = ch_getbufnr(a:channel, 'err')
    let msg = split(a:msg, '\n', 1)
    for i in range(len(msg))
        let pos  = getbufvar(buf, 'illume_pos')
        let line = getbufline(buf, pos)[0]
        call setbufline(buf, pos, line .. msg[i])
        if i < len(msg) - 1
            call appendbufline(buf, pos, '')
            if type(pos) == type(0)
                call setbufvar(buf, 'illume_pos', pos+1)
            endif
        endif
    endfor
endfunction

function! IllumeStop()
    try
        call job_stop(b:illume_job)
    catch
    endtry
    let b:illume_job = v:null
endfunction

function! IllumeStart()
    let b:illume_job = job_start(['illume'], #{
        \ in_io: 'buffer',
        \ in_buf: bufnr(),
        \ out_cb: function('IllumeAppend'),
        \ out_mode: 'raw',
        \ err_io: 'buffer',
        \ err_buf: bufnr(),
        \ })
endfunction

function! Illume()
    call IllumeStop()
    let b:illume_pos = '$'
    call IllumeStart()
endfunction

function! IllumeInfill()
    call IllumeStop()
    let b:illume_pos = line('.') + 1
    call appendbufline('', line('.'), '!infill')
    call IllumeStart()
    call setline(line('.')+1, '')
endfunction

map <leader>a :call Illume()<cr>
map <leader>f :call IllumeInfill()<cr>
map <leader>s :call IllumeStop()<cr>
map <leader>u Go<cr>!user<cr><cr>
