$(document).ready(function() {

    let qrcode = new QRCode("lightningqrcode", {
        text: document.getElementsByClassName("card-footer")[0].textContent,
        width: 300,
        height: 300
    });

    let counter = 0;

    while (counter < 300) {
        setTimeout(function () {
            $.getJSON("localhost:8080/check?data="+document.getElementsByClassName("card-footer")[0].textContent, function (result) {
                if (result["isPaid"]) {
                    window.location.replace("http://localhost:8080/");
                }
                counter++;
            });
        }, 1000);
    }

});