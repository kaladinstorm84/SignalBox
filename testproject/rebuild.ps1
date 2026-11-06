cd ..
taskkill /IM signalbox.exe /F
Start-Sleep -Seconds 2
go build ./cmd/signalbox
cd testproject 
..\signalbox.exe serve 