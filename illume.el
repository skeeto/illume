(defvar-local illume-process nil)

(defun illume-stop ()
  (interactive)
  (when illume-process
    (ignore-errors (kill-process illume-process))
    (setf illume-process nil)))

(defun illume ()
  (interactive)
  (illume-stop)
  (let ((process (make-process :name "illume"
                               :buffer (current-buffer)
                               :command '("illume")
                               :connection-type 'pipe
                               :sentinel (lambda (_ _)))))
    (setf illume-process process)
    (process-send-region process (point-min) (point-max))
    (process-send-eof process)))
